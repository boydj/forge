// Package health runs the node's local self-checks and drives the anycast
// announcement state (announced / drained / withdrawn) with hysteresis, so a
// POP that is broken at layer 7 stops attracting traffic and a flapping POP
// stays out longer each time. See docs/health.md and
// docs/network-architecture.md section 3.2.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Config tunes the health worker. Zero values are replaced by Default().
type Config struct {
	// Interval between check cycles.
	Interval time.Duration
	// FailuresToDrain is the number of consecutive failed cycles after which
	// an announced node drains (prepend + graceful-shutdown community).
	FailuresToDrain int
	// FailuresToWithdraw is the total number of consecutive failed cycles
	// after which the node withdraws its prefixes entirely.
	FailuresToWithdraw int
	// SuccessesToRecover is the number of consecutive healthy cycles needed
	// before a drained or withdrawn node may re-enter.
	SuccessesToRecover int
	// Cooldown is the minimum time since the last state change before a
	// recovery is allowed. It is the base value; flaps double it.
	Cooldown time.Duration
	// MaxBackoff caps the doubled cooldown.
	MaxBackoff time.Duration
	// FlapWindow: a health-driven drain that happens within this long of
	// the previous one doubles the cooldown; a longer quiet period resets it.
	FlapWindow time.Duration
	// Announcer is the path to scripts/bgp-announce. Empty disables BGP
	// control (single-node and development deployments).
	Announcer string
	// GeminiAddr and SSHAddr are local listener addresses to probe; empty
	// skips the respective check.
	GeminiAddr string
	SSHAddr    string
	// Hostname is used as the authority in the Gemini self-request and as
	// the TLS server name.
	Hostname string
	// DataDir is the filesystem whose free space is checked; empty skips.
	DataDir string
	// DiskMin is the minimum free bytes on DataDir.
	DiskMin int64
	// ReplicaLagMax fails the cycle when replication lag exceeds it;
	// 0 disables the check (the lag function is still not required).
	ReplicaLagMax int64
	// CheckTimeout bounds each individual check.
	CheckTimeout time.Duration
}

// Default returns the documented defaults: 10 s cycles, drain after 3
// failures (30 s), withdraw after 6 (60 s), recover after 6 successes
// (60 s) and a 120 s cooldown that doubles per flap up to 900 s.
func Default() Config {
	return Config{
		Interval:           10 * time.Second,
		FailuresToDrain:    3,
		FailuresToWithdraw: 6,
		SuccessesToRecover: 6,
		Cooldown:           120 * time.Second,
		MaxBackoff:         900 * time.Second,
		FlapWindow:         time.Hour,
		DiskMin:            1 << 30,
		CheckTimeout:       5 * time.Second,
	}
}

// withDefaults fills zero fields from Default().
func (c Config) withDefaults() Config {
	d := Default()
	if c.Interval <= 0 {
		c.Interval = d.Interval
	}
	if c.FailuresToDrain <= 0 {
		c.FailuresToDrain = d.FailuresToDrain
	}
	if c.FailuresToWithdraw <= 0 {
		c.FailuresToWithdraw = d.FailuresToWithdraw
	}
	if c.FailuresToWithdraw < c.FailuresToDrain {
		c.FailuresToWithdraw = c.FailuresToDrain
	}
	if c.SuccessesToRecover <= 0 {
		c.SuccessesToRecover = d.SuccessesToRecover
	}
	if c.Cooldown < 0 {
		c.Cooldown = d.Cooldown
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = d.MaxBackoff
	}
	if c.MaxBackoff < c.Cooldown {
		c.MaxBackoff = c.Cooldown
	}
	if c.FlapWindow <= 0 {
		c.FlapWindow = d.FlapWindow
	}
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = d.CheckTimeout
	}
	return c
}

// State is the announcement state of this node.
type State int

const (
	// StateWithdrawn exports nothing (ANNOUNCE=false).
	StateWithdrawn State = iota
	// StateDrained exports with prepend and drain communities
	// (ANNOUNCE=true DRAIN=true): reachable, but not the best path.
	StateDrained
	// StateAnnounced exports normally (ANNOUNCE=true DRAIN=false).
	StateAnnounced
)

func (s State) String() string {
	switch s {
	case StateWithdrawn:
		return "withdrawn"
	case StateDrained:
		return "drained"
	case StateAnnounced:
		return "announced"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// Announcer applies announcement changes. The verbs match scripts/bgp-announce.
type Announcer interface {
	Announce(ctx context.Context) error
	Withdraw(ctx context.Context) error
	Drain(ctx context.Context) error
	Undrain(ctx context.Context) error
}

// NopAnnouncer does nothing; used when BGP control is disabled.
type NopAnnouncer struct{}

func (NopAnnouncer) Announce(context.Context) error { return nil }
func (NopAnnouncer) Withdraw(context.Context) error { return nil }
func (NopAnnouncer) Drain(context.Context) error    { return nil }
func (NopAnnouncer) Undrain(context.Context) error  { return nil }

// ExecAnnouncer runs scripts/bgp-announce with one verb per call.
type ExecAnnouncer struct {
	// Path to the script.
	Path string
	// Timeout per invocation (default 20 s; the script runs birdc twice).
	Timeout time.Duration
	// Env, when non-nil, replaces the process environment (tests).
	Env []string
}

// NewAnnouncer returns an ExecAnnouncer for path, or NopAnnouncer when path
// is empty.
func NewAnnouncer(path string) Announcer {
	if path == "" {
		return NopAnnouncer{}
	}
	return &ExecAnnouncer{Path: path}
}

func (e *ExecAnnouncer) Announce(ctx context.Context) error { return e.run(ctx, "announce") }
func (e *ExecAnnouncer) Withdraw(ctx context.Context) error { return e.run(ctx, "withdraw") }
func (e *ExecAnnouncer) Drain(ctx context.Context) error    { return e.run(ctx, "drain") }
func (e *ExecAnnouncer) Undrain(ctx context.Context) error  { return e.run(ctx, "undrain") }

func (e *ExecAnnouncer) run(ctx context.Context, verb string) error {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Path, verb)
	if e.Env != nil {
		cmd.Env = e.Env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (timeout %s)", err, timeout)
		}
		if msg != "" {
			return fmt.Errorf("%s %s: %w: %s", filepath.Base(e.Path), verb, err, msg)
		}
		return fmt.Errorf("%s %s: %w", filepath.Base(e.Path), verb, err)
	}
	return nil
}

// Metrics publishes the two health gauges. A nil *Metrics or nil gauges are
// ignored.
type Metrics struct {
	// Healthy is forge_healthy: 1 when the last cycle passed every check.
	Healthy prometheus.Gauge
	// Announced is forge_bgp_announced: 1 only in StateAnnounced (a drained
	// node is still exporting but is not meant to carry traffic).
	Announced prometheus.Gauge
}

// SetHealthy sets forge_healthy.
func (m *Metrics) SetHealthy(ok bool) {
	if m != nil && m.Healthy != nil {
		m.Healthy.Set(b2f(ok))
	}
}

// SetAnnounced sets forge_bgp_announced.
func (m *Metrics) SetAnnounced(ok bool) {
	if m != nil && m.Announced != nil {
		m.Announced.Set(b2f(ok))
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func loggerOr(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}
