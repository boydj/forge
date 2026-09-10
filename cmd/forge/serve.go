package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/health"
	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/repl"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/tlsid"
	gitvcs "as215520.net/forge/internal/vcs/git"
	"as215520.net/forge/internal/version"
	"as215520.net/forge/internal/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", envOr("FORGE_CONFIG", "/etc/forge/forge.toml"), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	log.Info("forge starting", "version", version.Version, "node", cfg.Node, "hostname", cfg.Hostname, "data", cfg.DataDir)

	app, err := openForge(context.Background(), cfg, log)
	if err != nil {
		return err
	}
	defer app.Store.Close()
	if err := app.EnsureDirs(); err != nil {
		return err
	}
	if err := installHooks(cfg); err != nil {
		return err
	}

	names := []string{cfg.Hostname}
	if cfg.Hostname != "localhost" {
		names = append(names, "localhost")
	}
	cert, created, err := tlsid.LoadOrCreate(cfg.Gemini.CertFile, cfg.Gemini.KeyFile, names)
	if err != nil {
		return fmt.Errorf("tls identity: %w", err)
	}
	log.Info("tls identity", "created", created, "cert", tlsid.Describe(cert))

	reg := metrics.New()
	handler := web.New(app, log)
	hc := health.Default()
	hc.Announcer, hc.Interval = cfg.Health.Announcer, cfg.Health.Interval.Duration
	hc.ReplicaLagMax = cfg.Health.ReplicaLagMax
	hc.GeminiAddr, hc.SSHAddr = probeAddr(cfg.Gemini.Listen[0]), ""
	if len(cfg.SSH.Listen) > 0 {
		hc.SSHAddr = probeAddr(cfg.SSH.Listen[0])
	}
	hc.Hostname, hc.DataDir, hc.DiskMin = cfg.Hostname, cfg.DataDir, cfg.Limits.MinFreeBytes
	hw := health.New(hc, health.BuiltinChecks(hc, health.PingFunc(app.Store.DB().PingContext), nil), health.NewAnnouncer(hc.Announcer)).
		SetLogger(log.With("proto", "health")).SetMetrics(&health.Metrics{Healthy: reg.Healthy, Announced: reg.Announced})
	handler.Health = hw.Healthy
	gsrv := &gemini.Server{
		Handler:       handler,
		TLSConfig:     gemini.TLSServerConfig(cert),
		MaxTitanBody:  cfg.Limits.MaxTitanBytes,
		MaxConns:      cfg.Limits.MaxConns,
		MaxConnsPerIP: cfg.Limits.MaxConnsPerIP,
		Logger:        log.With("proto", "gemini"),
		Metrics:       reg.Gemini(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 8)

	var rn *repl.Node
	if cfg.Cluster.Enabled {
		rn, err = repl.New(repl.Options{
			Name: cfg.Node, Listen: cfg.Cluster.ControlListen, Peers: cfg.Cluster.Peers,
			SecretFile: cfg.Cluster.SecretFile, MetadataLeader: cfg.Cluster.MetadataLeader,
			SyncInterval: cfg.Cluster.SyncInterval.Duration, ReposDir: cfg.ReposDir(), Version: version.Version,
			Store: app.Store, Git: app.Git, Log: log.With("proto", "repl"), Metrics: reg, Forward: handler,
		})
		if err != nil {
			return fmt.Errorf("replication: %w", err)
		}
		app.OnPush = rn.OnPush
		handler.Forwarder = rn
		if err := rn.Start(ctx, errc); err != nil {
			return fmt.Errorf("replication start: %w", err)
		}
		log.Info("replication enabled", "control", cfg.Cluster.ControlListen, "peers", len(cfg.Cluster.Peers), "metadata_leader", rn.MetadataLeader())
	}

	for _, addr := range cfg.Gemini.Listen {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("gemini listen %s: %w", addr, err)
		}
		log.Info("listening", "proto", "gemini", "addr", l.Addr())
		go func() { errc <- gsrv.Serve(l) }()
	}

	sshShutdown, err := startSSH(ctx, cfg, app, reg, log, errc)
	if err != nil {
		return err
	}

	var msrv *http.Server
	if cfg.Metrics.Listen != "" {
		msrv = &http.Server{Addr: cfg.Metrics.Listen, Handler: reg.Handler(), ReadHeaderTimeout: 5 * time.Second}
		l, err := net.Listen("tcp", cfg.Metrics.Listen)
		if err != nil {
			return fmt.Errorf("metrics listen: %w", err)
		}
		log.Info("listening", "proto", "metrics", "addr", l.Addr())
		go func() { errc <- msrv.Serve(l) }()
	}

	go maintenanceLoop(ctx, app, log)
	hctx, hcancel := context.WithCancel(context.Background())
	hwDone := make(chan struct{})
	go func() { defer close(hwDone); hw.Run(hctx) }()
	go drainFileLoop(ctx, cfg.DrainFile(), hw, log)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listener failed", "err", err)
		}
	}
	// Drain the announcement first so new connections stop arriving, then
	// close listeners and finish in-flight requests.
	hcancel()
	<-hwDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = gsrv.Shutdown(shutdownCtx)
	if rn != nil {
		_ = rn.Shutdown(shutdownCtx)
	}
	if sshShutdown != nil {
		_ = sshShutdown(shutdownCtx)
	}
	if msrv != nil {
		_ = msrv.Shutdown(shutdownCtx)
	}
	return nil
}

func openForge(ctx context.Context, cfg *config.Config, log *slog.Logger) (*forge.Forge, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DBPath(), cfg.Node)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	g, err := gitvcs.New(gitvcs.Options{
		Binary:        cfg.Git.Binary,
		Timeout:       cfg.Git.Timeout.Duration,
		MaxConcurrent: cfg.Git.MaxConcurrent,
		HooksDir:      filepath.Join(cfg.DataDir, "hooks"),
		HomeDir:       cfg.DataDir,
		MaxInputSize:  cfg.Limits.MaxPushBytes,
	})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	return forge.New(cfg, st, g, log), nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// probeAddr turns a listen address into a dialable one (":1965" -> "127.0.0.1:1965").
func probeAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// drainFileLoop pins the node drained while the operator marker file
// exists (`forge admin pop drain`), and releases it when removed.
func drainFileLoop(ctx context.Context, path string, hw *health.Controller, log *slog.Logger) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	pinned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, err := os.Stat(path)
			exists := err == nil
			switch {
			case exists && !pinned:
				log.Warn("operator drain requested", "file", path)
				if err := hw.Drain(ctx); err != nil {
					log.Error("drain failed", "err", err)
					continue
				}
				pinned = true
			case !exists && pinned:
				log.Info("operator drain released", "file", path)
				if err := hw.Undrain(ctx); err != nil {
					log.Error("undrain failed", "err", err)
					continue
				}
				pinned = false
			}
		}
	}
}

// maintenanceLoop runs periodic housekeeping: purge deleted repositories,
// expired tokens, refresh sizes.
func maintenanceLoop(ctx context.Context, app *forge.Forge, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := app.PurgeDeleted(ctx); err != nil {
				log.Error("purge deleted", "err", err)
			} else if n > 0 {
				log.Info("purged deleted repositories", "count", n)
			}
			if err := app.Store.PurgeTokens(ctx); err != nil {
				log.Error("purge tokens", "err", err)
			}
		}
	}
}

// installHooks writes the git hook scripts that call back into this binary.
func installHooks(cfg *config.Config) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.DataDir, "hooks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, name := range []string{"pre-receive", "update", "post-receive", "proc-receive"} {
		script := fmt.Sprintf("#!/bin/sh\nexec %q hook %s \"$@\"\n", self, name)
		p := filepath.Join(dir, name)
		if cur, err := os.ReadFile(p); err == nil && string(cur) == script {
			continue
		}
		if err := os.WriteFile(p+".tmp", []byte(script), 0o755); err != nil {
			return err
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			return err
		}
	}
	return nil
}
