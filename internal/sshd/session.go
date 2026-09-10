package sshd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// session is one accepted "session" channel. It accepts at most one exec.
type session struct {
	srv    *Server
	log    *slog.Logger
	acct   *Account
	remote string
	ch     ssh.Channel

	gitProtocolV2 bool
	execDone      chan struct{} // closed when the exec goroutine has finished
	stdinDone     chan struct{} // closed when the stdin copier has finished
}

// Request payloads (RFC 4254 section 6).
type envRequest struct {
	Name  string
	Value string
}

type execRequest struct {
	Command string
}

type exitStatus struct {
	Status uint32
}

// run services channel requests until the channel closes. The exec runs in
// its own goroutine so that requests arriving during the transfer are still
// answered (refused) promptly and cannot stall the connection.
func (s *session) run(reqs <-chan *ssh.Request) {
	defer s.ch.Close()
	for req := range reqs {
		switch req.Type {
		case "env":
			var er envRequest
			ok := false
			if s.execDone == nil {
				if err := ssh.Unmarshal(req.Payload, &er); err == nil &&
					er.Name == "GIT_PROTOCOL" && er.Value == "version=2" {
					s.gitProtocolV2 = true
					ok = true
				}
			}
			s.reply(req, ok)
		case "exec":
			if s.execDone != nil {
				s.reply(req, false)
				continue
			}
			var xr execRequest
			if err := ssh.Unmarshal(req.Payload, &xr); err != nil {
				s.reply(req, false)
				s.finish(1)
				continue
			}
			// Accept the request so the client reads what follows, then
			// run (or refuse with a message) and finish with exit-status.
			s.reply(req, true)
			s.execDone = make(chan struct{})
			go func() {
				defer close(s.execDone)
				s.finish(s.exec(xr.Command))
			}()
		case "shell":
			if s.execDone != nil {
				s.reply(req, false)
				continue
			}
			// Explain first so the message is displayed, then refuse.
			s.log.Info("ssh shell refused")
			s.stderr("forge: interactive shell not available; use git")
			s.reply(req, false)
			s.execDone = make(chan struct{})
			close(s.execDone)
			s.finish(1)
		default:
			// pty-req, subsystem (sftp), x11-req, auth-agent-req@openssh.com,
			// signal, window-change, break, ... are all refused.
			s.log.Debug("ssh session request refused", "type", req.Type)
			s.reply(req, false)
		}
	}
	if s.execDone != nil {
		<-s.execDone
	}
	if s.stdinDone != nil {
		_ = s.ch.Close()
		<-s.stdinDone
	}
}

func (s *session) reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

func (s *session) stderr(msg string) {
	_, _ = io.WriteString(s.ch.Stderr(), msg+"\n")
}

// finish reports the exit status and closes the channel in the order
// OpenSSH's sshd uses: exit-status, EOF, close.
func (s *session) finish(code int) {
	if code < 0 || code > 255 {
		code = 255
	}
	_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: uint32(code)}))
	_ = s.ch.CloseWrite()
	_ = s.ch.Close()
}

// exec parses and authorizes the command, runs git and returns the exit
// code to report. Every refusal is explained on stderr.
func (s *session) exec(line string) int {
	start := time.Now()
	srv := s.srv
	log := s.log
	opName := "invalid"
	success := false
	code := 1
	defer func() {
		d := time.Since(start)
		if srv.Metrics != nil {
			srv.Metrics.ObserveSession(opName, success, d)
		}
	}()

	cmd, err := ParseCommand(line)
	if err != nil {
		log.Info("ssh exec refused", "command", truncate(line, 80), "err", err)
		s.stderr("forge: " + stripPrefix(err))
		return 1
	}
	opName = cmd.Op.String()
	log = log.With("op", opName, "repo", cmd.Path())

	ctx, cancel := context.WithTimeout(context.Background(), srv.Config.SessionTimeout)
	defer cancel()

	diskPath, err := srv.Authz.Authorize(ctx, s.acct, cmd.Owner, cmd.Repo, cmd.Op)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoRepo):
			s.stderr(fmt.Sprintf("forge: repository '%s' not found", cmd.Path()))
		case errors.Is(err, ErrForbidden):
			msg := fmt.Sprintf("forge: forbidden: write access to '%s' denied", cmd.Path())
			if detail := strings.TrimPrefix(err.Error(), ErrForbidden.Error()); detail != "" && detail != err.Error() {
				msg += strings.TrimSuffix(detail, "\n")
			}
			s.stderr(msg)
		default:
			log.Error("authorizer error", "err", err)
			s.stderr("forge: internal error")
		}
		log.Info("ssh exec denied", "err", err, "ms", time.Since(start).Milliseconds())
		return 1
	}

	// A concurrency slot for the git subprocess; do not queue for long.
	slotCtx, slotCancel := context.WithTimeout(ctx, 10*time.Second)
	release, err := srv.Git.Acquire(slotCtx)
	slotCancel()
	if err != nil {
		s.stderr("forge: server busy, try again later")
		log.Warn("ssh exec refused: no git slot")
		return 1
	}
	defer release()

	var args []string
	switch cmd.Op {
	case OpRead:
		secs := int(srv.Config.IdleTimeout / time.Second)
		if secs <= 0 {
			secs = 1
		}
		args = []string{"upload-pack", "--strict", "--timeout=" + strconv.Itoa(secs), "--", diskPath}
	case OpWrite:
		args = []string{"receive-pack", "--", diskPath}
	}
	proc := srv.Git.Command(ctx, "", args...)
	proc.Env = append(proc.Env,
		"FORGE_ACCOUNT_ID="+strconv.FormatInt(s.acct.ID, 10),
		"FORGE_ACCOUNT="+s.acct.Name,
		"FORGE_REPO="+cmd.Path(),
		"FORGE_HOOK_SOCKET="+srv.Config.HookSocket,
		"FORGE_OP="+opName,
		"FORGE_REMOTE_IP="+s.remote,
	)
	if s.gitProtocolV2 {
		proc.Env = append(proc.Env, "GIT_PROTOCOL=version=2")
	}
	// stdout/stderr: exec's own copy goroutines, so Wait returns only after
	// all output has been written to the channel (bounded by WaitDelay).
	proc.Stdout = s.ch
	proc.Stderr = s.ch.Stderr()
	// stdin: our goroutine. Client EOF closes the pipe so upload-pack
	// finishes; when the process exits the channel is closed by finish,
	// which unblocks the copier if the client never sent EOF.
	stdin, err := proc.StdinPipe()
	if err != nil {
		s.stderr("forge: internal error")
		log.Error("stdin pipe", "err", err)
		return 1
	}
	if err := proc.Start(); err != nil {
		s.stderr("forge: internal error")
		log.Error("git start failed", "err", err)
		return 1
	}
	s.stdinDone = make(chan struct{})
	go func() {
		defer close(s.stdinDone)
		_, _ = io.Copy(stdin, s.ch)
		_ = stdin.Close()
	}()

	werr := proc.Wait()
	code = exitCodeOf(proc, werr)
	timedOut := ctx.Err() != nil
	if timedOut {
		s.stderr("forge: session timed out")
	}
	success = code == 0 && !timedOut
	log.Info("ssh session",
		"exit", code, "ms", time.Since(start).Milliseconds(),
		"timeout", timedOut, "protocol", protocolName(s.gitProtocolV2))
	return code
}

func exitCodeOf(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
		return 255 // killed by signal
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return 0
	}
	return 255
}

func protocolName(v2 bool) string {
	if v2 {
		return "v2"
	}
	return "v0"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripPrefix returns the part of a ParseCommand error after ErrBadCommand.
func stripPrefix(err error) string {
	msg := err.Error()
	base := ErrBadCommand.Error() + ": "
	if len(msg) > len(base) && msg[:len(base)] == base {
		return msg[len(base):]
	}
	return msg
}
