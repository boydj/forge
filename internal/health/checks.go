package health

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Check is one named self-check. Run must respect ctx (it carries the
// per-check timeout).
type Check struct {
	Name string
	Run  func(ctx context.Context) error
}

// Result is the outcome of one check in one cycle.
type Result struct {
	Name     string
	Err      error
	Duration time.Duration
}

// OK reports whether the check passed.
func (r Result) OK() bool { return r.Err == nil }

// Pinger is satisfied by *sql.DB (PingContext) via a one-line adapter, so the
// store package need not be imported here.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingFunc adapts a function to Pinger (e.g. store.DB().PingContext).
type PingFunc func(ctx context.Context) error

// Ping implements Pinger.
func (f PingFunc) Ping(ctx context.Context) error { return f(ctx) }

// Checker runs a set of checks in parallel, each under its own timeout.
type Checker struct {
	Checks  []Check
	Timeout time.Duration
}

// Run executes every check concurrently and returns results in the same
// order as Checks. A cycle is healthy iff every result is OK.
func (c *Checker) Run(ctx context.Context) []Result {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = Default().CheckTimeout
	}
	results := make([]Result, len(c.Checks))
	var wg sync.WaitGroup
	for i, chk := range c.Checks {
		wg.Add(1)
		go func(i int, chk Check) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			err := runCheck(cctx, chk)
			results[i] = Result{Name: chk.Name, Err: err, Duration: time.Since(start)}
		}(i, chk)
	}
	wg.Wait()
	return results
}

// runCheck enforces the timeout even for checks that ignore ctx, and turns
// panics into failures so one bad check cannot kill the daemon.
func runCheck(ctx context.Context, chk Check) (err error) {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- chk.Run(ctx)
	}()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout: %w", ctx.Err())
	}
}

// AllOK reports whether every result passed.
func AllOK(rs []Result) bool {
	for _, r := range rs {
		if r.Err != nil {
			return false
		}
	}
	return true
}

// Failing summarises failing results as "name: err, name: err", or "ok".
func Failing(rs []Result) string {
	var parts []string
	for _, r := range rs {
		if r.Err != nil {
			parts = append(parts, r.Name+": "+r.Err.Error())
		}
	}
	if len(parts) == 0 {
		return "ok"
	}
	return strings.Join(parts, "; ")
}

// BuiltinChecks assembles the standard checks for cfg. db and lag may be nil;
// checks whose configuration is empty are skipped.
func BuiltinChecks(cfg Config, db Pinger, lag func() (int64, error)) []Check {
	var checks []Check
	if cfg.GeminiAddr != "" {
		checks = append(checks, GeminiCheck(cfg.GeminiAddr, cfg.Hostname))
	}
	if cfg.SSHAddr != "" {
		checks = append(checks, SSHCheck(cfg.SSHAddr))
	}
	if cfg.DataDir != "" {
		checks = append(checks, DiskCheck(cfg.DataDir, cfg.DiskMin))
	}
	if db != nil {
		checks = append(checks, DBCheck(db))
	}
	if lag != nil && cfg.ReplicaLagMax > 0 {
		checks = append(checks, ReplicaLagCheck(lag, cfg.ReplicaLagMax))
	}
	return checks
}

// GeminiCheck opens a TLS connection to addr, requests the front page
// (gemini://<hostname>/) and expects a 20 response. It must not request
// /status: that page reports this controller's own verdict, which would
// make the check circular (a starting node could never become healthy). The certificate is
// self-signed (TOFU), so verification is skipped; the point is that the
// daemon answers, not who it is.
func GeminiCheck(addr, hostname string) Check {
	if hostname == "" {
		hostname = "localhost"
	}
	return Check{Name: "gemini", Run: func(ctx context.Context) error {
		d := &net.Dialer{}
		raw, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer raw.Close()
		if dl, ok := ctx.Deadline(); ok {
			_ = raw.SetDeadline(dl)
		}
		conn := tls.Client(raw, &tls.Config{
			ServerName:         hostname,
			InsecureSkipVerify: true, //nolint:gosec // self-signed TOFU identity; liveness probe only
			MinVersion:         tls.VersionTLS12,
		})
		if err := conn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		if _, err := fmt.Fprintf(conn, "gemini://%s/\r\n", hostname); err != nil {
			return err
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "20") {
			return fmt.Errorf("status %q", line)
		}
		return nil
	}}
}

// SSHCheck connects to addr and expects the "SSH-2.0-forge" banner.
func SSHCheck(addr string) Check {
	return Check{Name: "ssh", Run: func(ctx context.Context) error {
		d := &net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return fmt.Errorf("read banner: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "SSH-2.0-forge") {
			return fmt.Errorf("banner %q", line)
		}
		return nil
	}}
}

// ErrDiskLow is returned by DiskCheck when free space is below the minimum.
var ErrDiskLow = errors.New("free space below minimum")

// FreeDisk returns the free bytes available to unprivileged users on the
// filesystem holding path.
func FreeDisk(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// DiskCheck fails when the filesystem holding path has fewer than min free
// bytes.
func DiskCheck(path string, min int64) Check {
	return Check{Name: "disk", Run: func(context.Context) error {
		free, err := FreeDisk(path)
		if err != nil {
			return err
		}
		if free < min {
			return fmt.Errorf("%w: %d < %d bytes", ErrDiskLow, free, min)
		}
		return nil
	}}
}

// DBCheck pings the database.
func DBCheck(p Pinger) Check {
	return Check{Name: "db", Run: p.Ping}
}

// ReplicaLagCheck fails when lag() exceeds max events.
func ReplicaLagCheck(lag func() (int64, error), max int64) Check {
	return Check{Name: "replica", Run: func(context.Context) error {
		n, err := lag()
		if err != nil {
			return err
		}
		if n > max {
			return fmt.Errorf("lag %d > %d events", n, max)
		}
		return nil
	}}
}
