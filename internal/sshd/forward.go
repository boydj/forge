package sshd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Push forwarding. Under anycast a `git push` can reach a node that does
// not lead the repository (ADR 0011: one writer per repository). Instead of
// refusing it, the replica relays the receive-pack session to the leader
// over the control plane and the leader runs receive-pack exactly as it
// would for a local push: same hardened environment, same hooks, same
// identity. The trust model mirrors forwarded Titan writes
// (docs/replication.md): the replica asserts an identity it has already
// verified by SSH key, and the leader re-runs authorisation from scratch.

// NotLeaderError is returned by an Authorizer for a write to a repository
// led by another node. It wraps ErrForbidden so callers that do not forward
// still refuse the push with the leader's name.
type NotLeaderError struct {
	Leader string
}

func (e *NotLeaderError) Error() string {
	return fmt.Sprintf("%s: pushes for this repository are accepted by node %s", ErrForbidden.Error(), e.Leader)
}

// Unwrap makes errors.Is(err, ErrForbidden) true.
func (e *NotLeaderError) Unwrap() error { return ErrForbidden }

// ForwardedPush identifies one receive-pack session relayed to a leader.
type ForwardedPush struct {
	// AccountID, Account and Fingerprint are the identity the replica
	// authenticated by SSH key.
	AccountID   int64
	Account     string
	Fingerprint string
	// Owner and Repo name the repository.
	Owner, Repo string
	// RemoteIP is the pushing client's address as seen by the replica.
	RemoteIP string
	// GitProtocolV2 is set when the client asked for protocol v2.
	GitProtocolV2 bool
}

// PushForwarder relays a receive-pack session to leader and returns the
// exit status of the leader's receive-pack. A transport error (leader down
// or unreachable) is returned as err; nothing has been written to stdout in
// that case. *repl.Node implements it through an adapter in cmd/forge.
type PushForwarder interface {
	ForwardReceivePack(ctx context.Context, leader string, push ForwardedPush, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
}

// ForwardMetrics is implemented by a Metrics that also counts forwarded
// pushes. role is "replica" (this node relayed) or "leader" (this node ran
// the push for a peer); result is "ok", "rejected" or "unreachable".
type ForwardMetrics interface {
	ObserveForward(role, result string)
}

func (s *Server) observeForward(role, result string) {
	if m, ok := s.Metrics.(ForwardMetrics); ok && m != nil {
		m.ObserveForward(role, result)
	}
}

// ServeGit authorises and runs one git transport command for acct with the
// client's streams attached: the whole of what an SSH exec does after the
// command line has been parsed. It is shared by local SSH sessions and by
// pushes forwarded from a replica (ServeForwardedPush).
//
// Every refusal is explained on stderr except one: a write to a repository
// led by another node returns a *NotLeaderError before anything is written,
// so the caller can decide whether to forward it. The returned error is
// informational (the authorisation or start failure behind a non-zero exit)
// and must not be reported to the client again. stdin is read by a helper
// goroutine that ends when the caller closes the stream it came from.
func (s *Server) ServeGit(ctx context.Context, acct *Account, cmd Command, remoteIP string, protoV2 bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if err := s.init(); err != nil {
		return 1, err
	}
	start := time.Now()
	opName := cmd.Op.String()
	log := s.logger().With("account", acct.Name, "op", opName, "repo", cmd.Path(), "remote", remoteIP)
	say := func(msg string) { _, _ = io.WriteString(stderr, msg+"\n") }

	diskPath, err := s.Authz.Authorize(ctx, acct, cmd.Owner, cmd.Repo, cmd.Op)
	if err != nil {
		var nl *NotLeaderError
		if errors.As(err, &nl) {
			return 1, err
		}
		say(refusalMessage(cmd, err))
		if !errors.Is(err, ErrNoRepo) && !errors.Is(err, ErrForbidden) {
			log.Error("authorizer error", "err", err)
		}
		log.Info("git exec denied", "err", err, "ms", time.Since(start).Milliseconds())
		return 1, err
	}

	// A concurrency slot for the git subprocess; do not queue for long.
	slotCtx, slotCancel := context.WithTimeout(ctx, 10*time.Second)
	release, err := s.Git.Acquire(slotCtx)
	slotCancel()
	if err != nil {
		say("forge: server busy, try again later")
		log.Warn("git exec refused: no git slot")
		return 1, err
	}
	defer release()

	var args []string
	switch cmd.Op {
	case OpRead:
		secs := int(s.Config.IdleTimeout / time.Second)
		if secs <= 0 {
			secs = 1
		}
		args = []string{"upload-pack", "--strict", "--timeout=" + strconv.Itoa(secs), "--", diskPath}
	case OpWrite:
		args = []string{"receive-pack", "--", diskPath}
	}
	proc := s.Git.Command(ctx, "", args...)
	proc.Env = append(proc.Env,
		"FORGE_ACCOUNT_ID="+strconv.FormatInt(acct.ID, 10),
		"FORGE_ACCOUNT="+acct.Name,
		"FORGE_REPO="+cmd.Path(),
		"FORGE_HOOK_SOCKET="+s.Config.HookSocket,
		"FORGE_OP="+opName,
		"FORGE_REMOTE_IP="+remoteIP,
	)
	if protoV2 {
		proc.Env = append(proc.Env, "GIT_PROTOCOL=version=2")
	}
	// stdout/stderr: exec's own copy goroutines, so Wait returns only after
	// all output has been written (bounded by WaitDelay).
	proc.Stdout = stdout
	proc.Stderr = stderr
	// stdin: our goroutine. Client EOF closes the pipe so upload-pack
	// finishes; if the client never sends EOF the copier ends when the
	// caller closes the stream after we return.
	pipe, err := proc.StdinPipe()
	if err != nil {
		say("forge: internal error")
		log.Error("stdin pipe", "err", err)
		return 1, err
	}
	if err := proc.Start(); err != nil {
		say("forge: internal error")
		log.Error("git start failed", "err", err)
		return 1, err
	}
	go func() {
		_, _ = io.Copy(pipe, stdin)
		_ = pipe.Close()
	}()

	werr := proc.Wait()
	code := exitCodeOf(proc, werr)
	timedOut := ctx.Err() != nil
	if timedOut {
		say("forge: session timed out")
		if code == 0 {
			code = 1
		}
	}
	log.Info("git session", "exit", code, "ms", time.Since(start).Milliseconds(),
		"timeout", timedOut, "protocol", protocolName(protoV2))
	return code, nil
}

// ServeForwardedPush runs a receive-pack session relayed by a replica on
// this node, which should lead the repository. The identity comes from the
// replica; everything else (repository, permission, leadership, hooks) is
// decided here, exactly as for a local push. It returns the exit status to
// relay; every refusal is explained on stderr.
func (s *Server) ServeForwardedPush(ctx context.Context, push ForwardedPush, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := s.init(); err != nil {
		_, _ = io.WriteString(stderr, "forge: internal error\n")
		return 1
	}
	say := func(msg string) { _, _ = io.WriteString(stderr, msg+"\n") }
	// The replica parsed the command line; parse it again here so a peer
	// cannot hand us a path the grammar would have refused.
	cmd, err := ParseCommand("git-receive-pack '" + push.Owner + "/" + push.Repo + ".git'")
	if err != nil || push.AccountID <= 0 || push.Account == "" {
		say("forge: forwarded push rejected: invalid request")
		s.observeForward("leader", "rejected")
		return 1
	}
	acct := &Account{ID: push.AccountID, Name: push.Account, Fingerprint: push.Fingerprint}
	ctx, cancel := context.WithTimeout(ctx, s.Config.SessionTimeout)
	defer cancel()
	code, err := s.ServeGit(ctx, acct, cmd, push.RemoteIP, push.GitProtocolV2, stdin, stdout, stderr)
	var nl *NotLeaderError
	if errors.As(err, &nl) {
		// Leadership moved between the replica's check and ours.
		say(refusalMessage(cmd, err))
		code = 1
	}
	if code == 0 {
		s.observeForward("leader", "ok")
	} else {
		s.observeForward("leader", "rejected")
	}
	return code
}

// refusalMessage is the stderr line for an authorisation error.
func refusalMessage(cmd Command, err error) string {
	switch {
	case errors.Is(err, ErrNoRepo):
		return fmt.Sprintf("forge: repository '%s' not found", cmd.Path())
	case errors.Is(err, ErrForbidden):
		msg := fmt.Sprintf("forge: forbidden: write access to '%s' denied", cmd.Path())
		if detail := strings.TrimPrefix(err.Error(), ErrForbidden.Error()); detail != "" && detail != err.Error() {
			msg += strings.TrimSuffix(detail, "\n")
		}
		return msg
	}
	return "forge: internal error"
}
