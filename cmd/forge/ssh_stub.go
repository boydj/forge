//go:build !sshd

package main

import (
	"context"
	"log/slog"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/metrics"
)

// startSSH is replaced by ssh.go once the sshd package is integrated.
func startSSH(ctx context.Context, cfg *config.Config, app *forge.Forge, reg *metrics.Registry, log *slog.Logger, errc chan error) (func(context.Context) error, error) {
	log.Warn("ssh transport not built into this binary")
	return nil, nil
}
