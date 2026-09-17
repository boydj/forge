package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/hooks"
	"as215520.net/forge/internal/repl"
	"as215520.net/forge/internal/sshd"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/web"
	"as215520.net/forge/pkg/config"
	"as215520.net/forge/pkg/metrics"
)

// sshAuth adapts the forge to the SSH server's authentication and
// authorisation interfaces.
type sshAuth struct{ f *forge.Forge }

func (a sshAuth) AuthenticateKey(ctx context.Context, key ssh.PublicKey) (*sshd.Account, error) {
	u, k, err := a.f.UserForSSHKey(ctx, key)
	if err != nil {
		return nil, sshd.ErrUnknownKey
	}
	return &sshd.Account{ID: u.ID, Name: u.Name, Fingerprint: k.Fingerprint}, nil
}

func (a sshAuth) Authorize(ctx context.Context, acct *sshd.Account, owner, repo string, op sshd.Op) (string, error) {
	u, err := a.f.Store.UserByID(ctx, acct.ID)
	if err != nil || u.Disabled {
		return "", sshd.ErrNoRepo
	}
	// Authorize runs after signature verification; the key callback runs for
	// unverified queries too, so last-used is stamped here.
	if k, err := a.f.Store.SSHKeyByFingerprint(ctx, acct.Fingerprint); err == nil {
		_ = a.f.Store.TouchSSHKey(ctx, k.ID)
	}
	acc, err := a.f.LookupRepo(ctx, u, owner, repo)
	if err != nil {
		return "", sshd.ErrNoRepo
	}
	if op == sshd.OpWrite {
		// Readers may open receive-pack to propose changes (refs/for/*);
		// the pre-receive hook restricts what they can update.
		if acc.Repo.Archived {
			return "", fmt.Errorf("%w: repository is archived", sshd.ErrForbidden)
		}
		if !a.f.IsLeader(acc.Repo) {
			// The SSH server forwards the push to the leader when the
			// cluster is up, or refuses it with this name otherwise.
			return "", &sshd.NotLeaderError{Leader: acc.Repo.LeaderNode}
		}
	}
	return a.f.RepoPath(acc.Repo.Owner, acc.Repo.Name), nil
}

var _ store.Role

// pushForwarder adapts the replication node to sshd.PushForwarder (replica
// side: relay a push to the leader).
type pushForwarder struct{ n *repl.Node }

func (p pushForwarder) ForwardReceivePack(ctx context.Context, leader string, push sshd.ForwardedPush, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	proto := ""
	if push.GitProtocolV2 {
		proto = "version=2"
	}
	return p.n.ForwardPush(ctx, leader, repl.PushRequest{Owner: push.Owner, Repo: push.Repo, AccountID: push.AccountID,
		Account: push.Account, Fingerprint: push.Fingerprint, RemoteIP: push.RemoteIP, GitProtocol: proto}, stdin, stdout, stderr)
}

// pushHandler adapts the SSH server to repl.PushHandler (leader side: run a
// push relayed by a replica).
type pushHandler struct{ s *sshd.Server }

func (p pushHandler) ServeForwardedPush(ctx context.Context, req repl.PushRequest, stdin io.Reader, stdout, stderr io.Writer) int {
	return p.s.ServeForwardedPush(ctx, sshd.ForwardedPush{Owner: req.Owner, Repo: req.Repo, AccountID: req.AccountID,
		Account: req.Account, Fingerprint: req.Fingerprint, RemoteIP: req.RemoteIP, GitProtocolV2: req.GitProtocol == "version=2"}, stdin, stdout, stderr)
}

// startSSH starts the hook socket and the SSH listeners. When rn is set,
// pushes for repositories led elsewhere are relayed through it and pushes
// relayed by peers are served. It returns a shutdown function.
func startSSH(ctx context.Context, cfg *config.Config, app *forge.Forge, reg *metrics.Registry, rn *repl.Node, log *slog.Logger, errc chan error) (func(context.Context) error, error) {
	hookSrv := &hooks.Server{Handler: app}
	if err := hookSrv.Listen(cfg.HookSocket()); err != nil {
		return nil, fmt.Errorf("hook socket: %w", err)
	}
	go func() {
		if err := hookSrv.Serve(); err != nil {
			errc <- err
		}
	}()

	hostKey, err := sshd.LoadOrCreateHostKey(cfg.SSH.HostKeyFile)
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	pub := hostKey.PublicKey()
	web.HostKeyLine = strings.TrimSpace(fmt.Sprintf("%s %s", cfg.Hostname, ssh.MarshalAuthorizedKey(pub)))
	log.Info("ssh host key", "fingerprint", ssh.FingerprintSHA256(pub), "sshfp", sshd.SSHFPRecords(pub))

	var previous []ssh.Signer
	if p := cfg.SSH.PreviousHostKeyFile; p != "" {
		if _, err := os.Stat(p); err == nil {
			k, err := sshd.LoadOrCreateHostKey(p)
			if err != nil {
				return nil, fmt.Errorf("previous ssh host key: %w", err)
			}
			previous = append(previous, k)
			log.Info("offering previous ssh host key during rotation", "fingerprint", ssh.FingerprintSHA256(k.PublicKey()))
		}
	}
	srv := &sshd.Server{
		Config: sshd.Config{
			MaxAuthTries:     cfg.SSH.MaxAuthTries,
			HandshakeTimeout: cfg.SSH.HandshakeTimeout.Duration,
			SessionTimeout:   cfg.SSH.SessionTimeout.Duration,
			IdleTimeout:      cfg.SSH.IdleTimeout.Duration,
			MaxConns:         cfg.Limits.SSHMaxConns,
			MaxConnsPerIP:    cfg.Limits.SSHMaxConnsPerIP,
			HookSocket:       cfg.HookSocket(),
		},
		Auth:             sshAuth{f: app},
		Authz:            sshAuth{f: app},
		Git:              app.Git,
		HostKey:          hostKey,
		PreviousHostKeys: previous,
		Logger:           log.With("proto", "ssh"),
		Metrics:          reg.SSH(),
	}
	if rn != nil {
		srv.Forwarder = pushForwarder{n: rn}
		rn.SetPushHandler(pushHandler{s: srv})
	}
	for _, addr := range cfg.SSH.Listen {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("ssh listen %s: %w", addr, err)
		}
		log.Info("listening", "proto", "ssh", "addr", l.Addr())
		go func() {
			if err := srv.Serve(l); err != nil && !errors.Is(err, net.ErrClosed) {
				errc <- err
			}
		}()
	}
	return func(ctx context.Context) error {
		err := srv.Shutdown(ctx)
		_ = hookSrv.Close()
		return err
	}, nil
}
