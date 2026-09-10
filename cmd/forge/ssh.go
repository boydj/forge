package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/hooks"
	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/sshd"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/web"
)

// sshAuth adapts the forge to the SSH server's authentication and
// authorisation interfaces.
type sshAuth struct{ f *forge.Forge }

func (a sshAuth) AuthenticateKey(ctx context.Context, key ssh.PublicKey) (*sshd.Account, error) {
	u, k, err := a.f.UserForSSHKey(ctx, key)
	if err != nil {
		return nil, sshd.ErrUnknownKey
	}
	_ = a.f.Store.TouchSSHKey(ctx, k.ID)
	return &sshd.Account{ID: u.ID, Name: u.Name}, nil
}

func (a sshAuth) Authorize(ctx context.Context, acct *sshd.Account, owner, repo string, op sshd.Op) (string, error) {
	u, err := a.f.Store.UserByID(ctx, acct.ID)
	if err != nil || u.Disabled {
		return "", sshd.ErrNoRepo
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
			return "", fmt.Errorf("%w: pushes for this repository are accepted by node %s", sshd.ErrForbidden, acc.Repo.LeaderNode)
		}
	}
	return a.f.RepoPath(acc.Repo.Owner, acc.Repo.Name), nil
}

var _ store.Role

// startSSH starts the hook socket and the SSH listeners. It returns a
// shutdown function.
func startSSH(ctx context.Context, cfg *config.Config, app *forge.Forge, reg *metrics.Registry, log *slog.Logger, errc chan error) (func(context.Context) error, error) {
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
		Auth:    sshAuth{f: app},
		Authz:   sshAuth{f: app},
		Git:     app.Git,
		HostKey: hostKey,
		Logger:  log.With("proto", "ssh"),
		Metrics: reg.SSH(),
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
