// Package config loads the forge configuration from TOML with environment
// overrides. Every option has a documented default so that
// `forge admin init` can write a complete, commented file.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the complete node configuration.
type Config struct {
	// Node is the name of this node (e.g. "ewr1"). It must be unique across
	// the deployment and is used in replication, metrics and logs.
	Node string `toml:"node"`
	// Hostname is the public service name used to build URLs
	// (e.g. "git.as215520.net"). Requests for other SNI names are still served.
	Hostname string `toml:"hostname"`
	// Title is the forge name shown on pages.
	Title string `toml:"title"`
	// DataDir holds the database, repositories and temporary files.
	DataDir string `toml:"data_dir"`
	// LogLevel is debug|info|warn|error.
	LogLevel string `toml:"log_level"`
	// LogFormat is json|text.
	LogFormat string `toml:"log_format"`

	Gemini  Gemini  `toml:"gemini"`
	SSH     SSH     `toml:"ssh"`
	Git     Git     `toml:"git"`
	Limits  Limits  `toml:"limits"`
	Metrics Metrics `toml:"metrics"`
	Cluster Cluster `toml:"cluster"`
	Health  Health  `toml:"health"`
	Docs    Docs    `toml:"docs"`
	Status  Status  `toml:"status"`
	Mirror  Mirror  `toml:"mirror"`
	// Mirrors lists repositories pushed to an external remote after every
	// push and on Mirror.Interval (docs/dogfooding.md, "Mirror strategy").
	Mirrors []MirrorTarget `toml:"mirrors"`
}

// Health configures self-checks and anycast announcement control.
type Health struct {
	// Announcer is the path to scripts/bgp-announce; empty disables BGP
	// control (checks still run and feed /status and metrics).
	Announcer string `toml:"announcer"`
	// Interval between check cycles.
	Interval Duration `toml:"interval"`
	// ReplicaLagMax marks the node unhealthy when replication lags more
	// than this many events (0: ignore).
	ReplicaLagMax int64 `toml:"replica_lag_max"`
}

// Gemini configures the Gemini/Titan listener.
type Gemini struct {
	// Listen addresses, e.g. [":1965"] or ["[2001:db8::1]:1965", "203.0.113.1:1965"].
	Listen []string `toml:"listen"`
	// CertFile and KeyFile hold the service TLS identity (PEM). If absent a
	// self-signed certificate is generated on first start.
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
	// Public port used in generated URLs when not 1965.
	Port int `toml:"port"`
	// ExtraHosts are additional hostnames served besides Hostname,
	// "localhost" and IP literals (e.g. a node's own name). Requests for any
	// other host are refused with status 53.
	ExtraHosts []string `toml:"extra_hosts"`
}

// SSH configures the Git-over-SSH listener.
type SSH struct {
	Listen []string `toml:"listen"`
	// HostKeyFile is the OpenSSH private host key (ed25519). Generated if absent.
	HostKeyFile string `toml:"host_key_file"`
	// PreviousHostKeyFile, when present, is also offered to clients during a
	// host-key rotation overlap (see docs/runbooks/rotate-ssh-host-key.md).
	PreviousHostKeyFile string `toml:"previous_host_key_file"`
	// Port used in clone instructions when not 22.
	Port int `toml:"port"`
	// User shown in clone URLs (the SSH login name is ignored by the server).
	User string `toml:"user"`
	// MaxAuthTries before the connection is dropped.
	MaxAuthTries int `toml:"max_auth_tries"`
	// HandshakeTimeout bounds key exchange and authentication.
	HandshakeTimeout Duration `toml:"handshake_timeout"`
	// SessionTimeout bounds a single git operation.
	SessionTimeout Duration `toml:"session_timeout"`
	// IdleTimeout closes sessions with no traffic.
	IdleTimeout Duration `toml:"idle_timeout"`
}

// Git configures the git subprocess environment.
type Git struct {
	// Binary is the git executable (default: "git" resolved from PATH at start).
	Binary string `toml:"binary"`
	// Timeout bounds read-only plumbing commands used for rendering.
	Timeout Duration `toml:"timeout"`
	// MaxConcurrent limits simultaneous git subprocesses.
	MaxConcurrent int `toml:"max_concurrent"`
}

// Limits are resource limits and quotas.
type Limits struct {
	// MaxRepoBytes is the per-repository size quota.
	MaxRepoBytes int64 `toml:"max_repo_bytes"`
	// MaxUserBytes is the per-user total quota.
	MaxUserBytes int64 `toml:"max_user_bytes"`
	// MaxReposPerUser caps repository creation.
	MaxReposPerUser int `toml:"max_repos_per_user"`
	// MaxPushBytes is git receive.maxInputSize.
	MaxPushBytes int64 `toml:"max_push_bytes"`
	// MaxTitanBytes is the largest Titan body accepted on any path.
	MaxTitanBytes int64 `toml:"max_titan_bytes"`
	// MaxTextBytes bounds issue/comment/review bodies.
	MaxTextBytes int64 `toml:"max_text_bytes"`
	// MaxAssetBytes bounds a single release asset.
	MaxAssetBytes int64 `toml:"max_asset_bytes"`
	// MaxRenderBytes bounds a blob rendered inline; larger blobs are offered raw.
	MaxRenderBytes int64 `toml:"max_render_bytes"`
	// MaxDiffBytes truncates rendered diffs.
	MaxDiffBytes int64 `toml:"max_diff_bytes"`
	// MinFreeBytes refuses writes when free disk space drops below this.
	MinFreeBytes int64 `toml:"min_free_bytes"`
	// MaxConns and MaxConnsPerIP bound Gemini/Titan concurrency.
	MaxConns      int `toml:"max_conns"`
	MaxConnsPerIP int `toml:"max_conns_per_ip"`
	// SSHMaxConns and SSHMaxConnsPerIP bound SSH concurrency.
	SSHMaxConns      int `toml:"ssh_max_conns"`
	SSHMaxConnsPerIP int `toml:"ssh_max_conns_per_ip"`
	// WriteRatePerMinute bounds Titan writes per account.
	WriteRatePerMinute int `toml:"write_rate_per_minute"`
	// MaxChangeBytes bounds one push by a reader proposing a change.
	MaxChangeBytes int64 `toml:"max_change_bytes"`
	// MaxOpenChangesPerUser bounds open changes per user per repository.
	MaxOpenChangesPerUser int `toml:"max_open_changes_per_user"`
	// MaxChangeCommits bounds commits in one change version.
	MaxChangeCommits int `toml:"max_change_commits"`
}

// Docs publishes a directory of a public repository hosted on this forge as
// the site documentation at /docs/. Publishing is a git push to that
// repository (docs/dogfooding.md).
type Docs struct {
	// Repo is the "owner/name" of the public repository; empty disables /docs/.
	Repo string `toml:"repo"`
	// Path is the subdirectory to publish; "" publishes the repository root.
	Path string `toml:"path"`
	// Ref is the branch or tag to read; empty = the repository's default branch.
	Ref string `toml:"ref"`
	// Incidents is the subdirectory of Path holding incident reports named
	// YYYY-MM-DD-slug.md; the status page lists the newest and serves them
	// as a feed. "" disables the list.
	Incidents string `toml:"incidents"`
}

// Status configures the fleet status page's alert source.
type Status struct {
	// PrometheusURL is the base URL of the monitoring host's Prometheus over
	// the control network (e.g. "http://[fda5:...::fa]:9090"); its firing
	// alerts are served as the /status/alerts gemfeed. Empty disables it.
	PrometheusURL string `toml:"prometheus_url"`
	// BackupDir is where forge-backup writes its archives (*.tar.gz.age);
	// the age of the newest one is forge_backup_age_seconds. Empty: not
	// reported (the gauge stays 0, which never alerts).
	BackupDir string `toml:"backup_dir"`
}

// Mirror configures outbound mirroring to external git remotes.
type Mirror struct {
	// KeyFile is the OpenSSH private key used for ssh mirrors
	// (/run/forge/secrets/mirror.key on nodes). A configured but missing
	// file disables mirroring with one log line at start.
	KeyFile string `toml:"key_file"`
	// KnownHostsFile pins the remotes' host keys (StrictHostKeyChecking=yes).
	KnownHostsFile string `toml:"known_hosts_file"`
	// Interval is the periodic reconcile of every mirror (default 1h);
	// pushes are also mirrored as they happen.
	Interval Duration `toml:"interval"`
}

// MirrorTarget is one repository -> remote mapping.
type MirrorTarget struct {
	// Repo is "owner/name".
	Repo string `toml:"repo"`
	// URL is an ssh (git@host:path or ssh://) or https remote.
	URL string `toml:"url"`
}

// IsSSH reports whether the remote is reached over ssh (needs the key).
func (m MirrorTarget) IsSSH() bool {
	return strings.HasPrefix(m.URL, "ssh://") || (!strings.Contains(m.URL, "://") && strings.Contains(m.URL, ":"))
}

// MirrorTargets returns the configured mirrors keyed by repository.
func (c *Config) MirrorTargets() map[string]string {
	out := make(map[string]string, len(c.Mirrors))
	for _, m := range c.Mirrors {
		out[m.Repo] = m.URL
	}
	return out
}

var mirrorRepoRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}/[a-z0-9][a-z0-9._-]{0,63}$`)

// Metrics configures the Prometheus endpoint (HTTP, private network only).
type Metrics struct {
	// Listen is the address for /metrics; empty disables. Bind it to a
	// control-network or loopback address only.
	Listen string `toml:"listen"`
}

// Cluster configures multi-node operation.
type Cluster struct {
	// Enabled turns on replication and leader forwarding.
	Enabled bool `toml:"enabled"`
	// ControlListen is the address for node-to-node RPC over the control
	// network (WireGuard). Must not be publicly reachable.
	ControlListen string `toml:"control_listen"`
	// Peers maps node name to control address (host:port).
	Peers map[string]string `toml:"peers"`
	// SecretFile holds the shared cluster secret used to authenticate peers
	// (generated by `forge admin cluster init`).
	SecretFile string `toml:"secret_file"`
	// SyncInterval is how often replicas poll leaders.
	SyncInterval Duration `toml:"sync_interval"`
	// MetadataLeader names the node that owns users, certificates and SSH
	// keys. Default: alphabetically first of this node and its peers.
	MetadataLeader string `toml:"metadata_leader"`
}

// Duration is a TOML-friendly time.Duration.
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// Default returns the default configuration for a data directory.
func Default(dataDir string) *Config {
	return &Config{
		Node:      "local",
		Hostname:  "localhost",
		Title:     "forge",
		DataDir:   dataDir,
		LogLevel:  "info",
		LogFormat: "text",
		Gemini: Gemini{
			Listen:   []string{":1965"},
			CertFile: filepath.Join(dataDir, "tls", "server.crt"),
			KeyFile:  filepath.Join(dataDir, "tls", "server.key"),
			Port:     1965,
		},
		SSH: SSH{
			Listen:           []string{":22"},
			HostKeyFile:      filepath.Join(dataDir, "ssh", "host_ed25519"),
			Port:             22,
			User:             "git",
			MaxAuthTries:     6,
			HandshakeTimeout: Duration{20 * time.Second},
			SessionTimeout:   Duration{30 * time.Minute},
			IdleTimeout:      Duration{5 * time.Minute},
		},
		Git: Git{
			Binary:        "git",
			Timeout:       Duration{30 * time.Second},
			MaxConcurrent: 16,
		},
		Limits: Limits{
			MaxRepoBytes:          2 << 30,
			MaxUserBytes:          10 << 30,
			MaxReposPerUser:       100,
			MaxPushBytes:          1 << 30,
			MaxTitanBytes:         64 << 20,
			MaxTextBytes:          256 << 10,
			MaxAssetBytes:         64 << 20,
			MaxRenderBytes:        512 << 10,
			MaxDiffBytes:          1 << 20,
			MinFreeBytes:          1 << 30,
			MaxConns:              1024,
			MaxConnsPerIP:         32,
			SSHMaxConns:           256,
			SSHMaxConnsPerIP:      16,
			WriteRatePerMinute:    30,
			MaxChangeBytes:        64 << 20,
			MaxOpenChangesPerUser: 10,
			MaxChangeCommits:      500,
		},
		Metrics: Metrics{Listen: ""},
		Cluster: Cluster{
			Enabled:      false,
			SyncInterval: Duration{10 * time.Second},
		},
		Health: Health{Interval: Duration{10 * time.Second}},
		Docs:   Docs{Path: "docs", Incidents: "incidents"},
		Mirror: Mirror{Interval: Duration{time.Hour}},
	}
}

// Load reads a TOML file over the defaults and applies FORGE_* environment
// overrides for a few operational fields.
func Load(path string) (*Config, error) {
	cfg := Default("/var/lib/forge")
	if path != "" {
		md, err := toml.DecodeFile(path, cfg)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, 0, len(undecoded))
			for _, k := range undecoded {
				keys = append(keys, k.String())
			}
			return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
		}
	}
	if v := os.Getenv("FORGE_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("FORGE_NODE"); v != "" {
		cfg.Node = v
	}
	if v := os.Getenv("FORGE_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ServesHost reports whether requests for host should be answered here.
func (c *Config) ServesHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || host == strings.ToLower(c.Hostname) || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return true
	}
	for _, h := range c.Gemini.ExtraHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// Validate checks invariants.
func (c *Config) Validate() error {
	var errs []error
	if len(c.HookSocket()) >= 100 {
		errs = append(errs, fmt.Errorf("data_dir is too long for a Unix socket path (%d bytes; limit is about 100): use a shorter data_dir", len(c.HookSocket())))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir is required"))
	}
	if c.Hostname == "" {
		errs = append(errs, errors.New("hostname is required"))
	}
	if c.Node == "" || strings.ContainsAny(c.Node, "/ \t") {
		errs = append(errs, errors.New("node must be a simple name"))
	}
	if len(c.Gemini.Listen) == 0 {
		errs = append(errs, errors.New("gemini.listen is required"))
	}
	if c.Limits.MaxTitanBytes < c.Limits.MaxAssetBytes {
		errs = append(errs, errors.New("limits.max_titan_bytes must be >= max_asset_bytes"))
	}
	if c.Cluster.Enabled && c.Cluster.ControlListen == "" {
		errs = append(errs, errors.New("cluster.control_listen is required when cluster.enabled"))
	}
	seen := map[string]bool{}
	for _, m := range c.Mirrors {
		if !mirrorRepoRe.MatchString(m.Repo) || strings.HasSuffix(m.Repo, ".git") {
			errs = append(errs, fmt.Errorf("mirrors: repo %q must be owner/name", m.Repo))
		}
		if seen[m.Repo] {
			errs = append(errs, fmt.Errorf("mirrors: repo %q listed twice", m.Repo))
		}
		seen[m.Repo] = true
		switch {
		case m.URL == "":
			errs = append(errs, fmt.Errorf("mirrors: %s has no url", m.Repo))
		case strings.HasPrefix(m.URL, "https://"), strings.HasPrefix(m.URL, "ssh://"):
		case strings.Contains(m.URL, "://"):
			errs = append(errs, fmt.Errorf("mirrors: %s: url must be ssh (git@host:path, ssh://) or https", m.Repo))
		case !strings.Contains(m.URL, ":") || strings.HasPrefix(m.URL, "/") || strings.HasPrefix(m.URL, "."):
			errs = append(errs, fmt.Errorf("mirrors: %s: url must be ssh (git@host:path, ssh://) or https", m.Repo))
		}
		if m.IsSSH() && c.Mirror.KeyFile == "" {
			errs = append(errs, fmt.Errorf("mirrors: %s uses ssh but mirror.key_file is empty", m.Repo))
		}
		if m.IsSSH() && c.Mirror.KnownHostsFile == "" {
			errs = append(errs, fmt.Errorf("mirrors: %s uses ssh but mirror.known_hosts_file is empty", m.Repo))
		}
	}
	if c.Mirror.Interval.Duration < 0 {
		errs = append(errs, errors.New("mirror.interval must not be negative"))
	}
	if u := c.Status.PrometheusURL; u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		errs = append(errs, errors.New("status.prometheus_url must start with http:// or https://"))
	}
	if d := c.Status.BackupDir; d != "" && !filepath.IsAbs(d) {
		errs = append(errs, errors.New("status.backup_dir must be an absolute path"))
	}
	if c.Docs.Repo != "" {
		owner, name, ok := strings.Cut(c.Docs.Repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			errs = append(errs, errors.New("docs.repo must be \"owner/name\""))
		}
	}
	for key, p := range map[string]string{"docs.path": c.Docs.Path, "docs.incidents": c.Docs.Incidents} {
		if strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
			errs = append(errs, fmt.Errorf("%s must be a relative path inside the repository", key))
		}
	}
	return errors.Join(errs...)
}

// Write writes the configuration as TOML.
func (c *Config) Write(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, "# forge configuration. See docs/operations.md for every key."); err != nil {
		return err
	}
	return toml.NewEncoder(f).Encode(c)
}

// Paths derived from DataDir.

// DBPath is the SQLite database file.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "forge.db") }

// ReposDir is the root of bare repositories, laid out as <user>/<repo>.git.
func (c *Config) ReposDir() string { return filepath.Join(c.DataDir, "repos") }

// TmpDir holds in-progress uploads on the same filesystem as the data.
func (c *Config) TmpDir() string { return filepath.Join(c.DataDir, "tmp") }

// AssetsDir holds release assets, laid out as <user>/<repo>/<release>/<name>.
func (c *Config) AssetsDir() string { return filepath.Join(c.DataDir, "assets") }

// DrainFile is the operator marker that pins the node in drained state.
func (c *Config) DrainFile() string { return filepath.Join(c.DataDir, "drain") }

// HookSocket is the Unix socket used by git hooks to reach the daemon.
func (c *Config) HookSocket() string { return filepath.Join(c.DataDir, "hook.sock") }

// GeminiURL builds a gemini:// URL for a path.
func (c *Config) GeminiURL(path string) string {
	host := c.Hostname
	if c.Gemini.Port != 0 && c.Gemini.Port != 1965 {
		host = fmt.Sprintf("%s:%d", c.Hostname, c.Gemini.Port)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "gemini://" + host + path
}

// TitanURL builds a titan:// URL for a path.
func (c *Config) TitanURL(path string) string {
	return "titan" + strings.TrimPrefix(c.GeminiURL(path), "gemini")
}

// CloneURL builds the SSH clone URL for user/repo.
func (c *Config) CloneURL(user, repo string) string {
	if c.SSH.Port != 0 && c.SSH.Port != 22 {
		return fmt.Sprintf("ssh://%s@%s:%d/%s/%s.git", c.SSH.User, c.Hostname, c.SSH.Port, user, repo)
	}
	return fmt.Sprintf("%s@%s:%s/%s.git", c.SSH.User, c.Hostname, user, repo)
}
