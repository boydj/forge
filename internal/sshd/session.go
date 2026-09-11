package sshd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
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
	// Closing the channel ends ServeGit's stdin copier if the client never
	// sent EOF.
	_ = s.ch.Close()
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

// exec parses the command, runs it through ServeGit and returns the exit
// code to report. A push for a repository led by another node is relayed to
// that node when a Forwarder is configured; otherwise it is refused with
// the leader's name. Every refusal is explained on stderr.
func (s *session) exec(line string) int {
	start := time.Now()
	srv := s.srv
	opName := "invalid"
	success := false
	code := 1
	defer func() {
		if srv.Metrics != nil {
			srv.Metrics.ObserveSession(opName, success, time.Since(start))
		}
	}()

	cmd, err := ParseCommand(line)
	if err != nil {
		s.log.Info("ssh exec refused", "command", truncate(line, 80), "err", err)
		s.stderr("forge: " + stripPrefix(err))
		return 1
	}
	opName = cmd.Op.String()

	ctx, cancel := context.WithTimeout(context.Background(), srv.Config.SessionTimeout)
	defer cancel()

	code, err = srv.ServeGit(ctx, s.acct, cmd, s.remote, s.gitProtocolV2, s.ch, s.ch, s.ch.Stderr())
	var nl *NotLeaderError
	if errors.As(err, &nl) {
		code = s.forward(ctx, cmd, nl)
	}
	success = code == 0 && ctx.Err() == nil
	return code
}

// forward relays a push to the repository's leader, or refuses it with the
// leader's name when no forwarder is configured or the leader cannot be
// reached.
func (s *session) forward(ctx context.Context, cmd Command, nl *NotLeaderError) int {
	srv := s.srv
	log := s.log.With("op", cmd.Op.String(), "repo", cmd.Path(), "leader", nl.Leader)
	if srv.Forwarder == nil {
		s.stderr(refusalMessage(cmd, nl))
		log.Info("ssh exec denied", "err", nl)
		return 1
	}
	start := time.Now()
	push := ForwardedPush{AccountID: s.acct.ID, Account: s.acct.Name, Fingerprint: s.acct.Fingerprint,
		Owner: cmd.Owner, Repo: cmd.Repo, RemoteIP: s.remote, GitProtocolV2: s.gitProtocolV2}
	code, err := srv.Forwarder.ForwardReceivePack(ctx, nl.Leader, push, s.ch, s.ch, s.ch.Stderr())
	if err != nil {
		// Keep the refusal the client would have seen without forwarding
		// and say why the relay failed; the detail stays in the log.
		s.stderr(refusalMessage(cmd, nl) + " (leader unreachable)")
		log.Warn("push forward failed", "err", err, "ms", time.Since(start).Milliseconds())
		srv.observeForward("replica", "unreachable")
		return 1
	}
	if code == 0 {
		srv.observeForward("replica", "ok")
	} else {
		srv.observeForward("replica", "rejected")
	}
	log.Info("push forwarded", "exit", code, "ms", time.Since(start).Milliseconds())
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
