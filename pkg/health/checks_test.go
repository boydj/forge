package health

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "forge.test"},
		DNSNames:     []string{"forge.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// geminiStub answers every request with status and records request lines.
func geminiStub(t *testing.T, status string) (addr string, lines *[]string) {
	t.Helper()
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	var got []string
	lines = &got
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				mu.Lock()
				got = append(got, line)
				mu.Unlock()
				_, _ = c.Write([]byte(status + "\r\nok\n"))
			}()
		}
	}()
	return l.Addr().String(), lines
}

func TestGeminiCheck(t *testing.T) {
	addr, lines := geminiStub(t, "20 text/plain; charset=utf-8")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := GeminiCheck(addr, "forge.test").Run(ctx); err != nil {
		t.Fatalf("gemini check: %v", err)
	}
	if len(*lines) != 1 || (*lines)[0] != "gemini://forge.test/\r\n" {
		t.Fatalf("request lines = %q", *lines)
	}

	bad, _ := geminiStub(t, "41 unhealthy: disk")
	err := GeminiCheck(bad, "").Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "41") {
		t.Fatalf("expected 41 failure, got %v", err)
	}

	// Nothing listening.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	if err := GeminiCheck(closed, "x").Run(ctx); err == nil {
		t.Fatal("expected connect failure")
	}
}

func TestSSHCheck(t *testing.T) {
	serve := func(banner string, hang bool) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				if hang {
					continue // never write; the check must time out
				}
				_, _ = c.Write([]byte(banner))
				_ = c.Close()
			}
		}()
		return l.Addr().String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := SSHCheck(serve("SSH-2.0-forge\r\n", false)).Run(ctx); err != nil {
		t.Fatalf("ssh check: %v", err)
	}
	if err := SSHCheck(serve("SSH-2.0-OpenSSH_9.9\r\n", false)).Run(ctx); err == nil {
		t.Fatal("expected banner mismatch")
	}
	short, c2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer c2()
	start := time.Now()
	if err := SSHCheck(serve("", true)).Run(short); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("check did not honour the context deadline")
	}
}

func TestDiskDBAndLagChecks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := DiskCheck(dir, 1).Run(ctx); err != nil {
		t.Fatalf("disk: %v", err)
	}
	err := DiskCheck(dir, 1<<62).Run(ctx)
	if !errors.Is(err, ErrDiskLow) {
		t.Fatalf("expected ErrDiskLow, got %v", err)
	}
	if err := DiskCheck(filepath.Join(dir, "missing"), 1).Run(ctx); err == nil {
		t.Fatal("expected statfs error")
	}

	pingErr := errors.New("db closed")
	if err := DBCheck(PingFunc(func(context.Context) error { return nil })).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := DBCheck(PingFunc(func(context.Context) error { return pingErr })).Run(ctx); !errors.Is(err, pingErr) {
		t.Fatalf("db: %v", err)
	}

	lag := int64(0)
	chk := ReplicaLagCheck(func() (int64, error) { return lag, nil }, 100)
	if err := chk.Run(ctx); err != nil {
		t.Fatal(err)
	}
	lag = 101
	if err := chk.Run(ctx); err == nil {
		t.Fatal("expected lag failure")
	}
}

func TestCheckerParallelTimeoutsAndPanics(t *testing.T) {
	var mu sync.Mutex
	var running, maxRunning int
	slow := func(ctx context.Context) error {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		defer func() { mu.Lock(); running--; mu.Unlock() }()
		select {
		case <-time.After(50 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ck := &Checker{Timeout: 200 * time.Millisecond, Checks: []Check{
		{Name: "a", Run: slow}, {Name: "b", Run: slow}, {Name: "c", Run: slow},
		{Name: "stuck", Run: func(context.Context) error { time.Sleep(2 * time.Second); return nil }},
		{Name: "panics", Run: func(context.Context) error { panic("boom") }},
	}}
	start := time.Now()
	rs := ck.Run(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("checker took %s; per-check timeout not enforced", d)
	}
	if maxRunning < 2 {
		t.Fatalf("checks did not run in parallel (max %d)", maxRunning)
	}
	if AllOK(rs) {
		t.Fatal("expected failures")
	}
	byName := map[string]Result{}
	for _, r := range rs {
		byName[r.Name] = r
	}
	for _, n := range []string{"a", "b", "c"} {
		if !byName[n].OK() {
			t.Fatalf("%s failed: %v", n, byName[n].Err)
		}
	}
	if err := byName["stuck"].Err; err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("stuck: %v", err)
	}
	if err := byName["panics"].Err; err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panics: %v", err)
	}
	if s := Failing(rs); !strings.Contains(s, "stuck:") || !strings.Contains(s, "panics:") {
		t.Fatalf("Failing = %q", s)
	}
	if Failing(nil) != "ok" {
		t.Fatal("Failing(nil)")
	}
}

func TestBuiltinChecksSelection(t *testing.T) {
	cfg := Default()
	if n := len(BuiltinChecks(cfg, nil, nil)); n != 0 {
		t.Fatalf("empty config produced %d checks", n)
	}
	cfg.GeminiAddr, cfg.SSHAddr, cfg.DataDir = "127.0.0.1:1965", "127.0.0.1:2222", t.TempDir()
	cfg.ReplicaLagMax = 10
	cs := BuiltinChecks(cfg, PingFunc(func(context.Context) error { return nil }), func() (int64, error) { return 0, nil })
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "gemini,ssh,disk,db,replica" {
		t.Fatalf("checks = %s", got)
	}
}

// TestExecAnnouncerWithRealScript drives scripts/bgp-announce with
// BGP_NO_RELOAD=1 against a temporary state file.
func TestExecAnnouncerWithRealScript(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "bgp-announce"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skip("scripts/bgp-announce not found")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	state := filepath.Join(t.TempDir(), "state.conf")
	a := &ExecAnnouncer{Path: script, Env: append(os.Environ(), "BGP_STATE_FILE="+state, "BGP_NO_RELOAD=1")}
	ctx := context.Background()
	read := func() string {
		b, err := os.ReadFile(state)
		if err != nil {
			return "missing"
		}
		var ann, drain string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "define ANNOUNCE") {
				ann = l
			}
			if strings.HasPrefix(l, "define DRAIN") {
				drain = l
			}
		}
		return ann + " " + drain
	}
	// drain while withdrawn is refused by the script: surfaces as an error.
	if err := a.Drain(ctx); err == nil || !strings.Contains(err.Error(), "not announcing") {
		t.Fatalf("drain while withdrawn: %v", err)
	}
	if err := a.Announce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "define ANNOUNCE = true; define DRAIN = false;" {
		t.Fatalf("after announce: %s", got)
	}
	if err := a.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "define ANNOUNCE = true; define DRAIN = true;" {
		t.Fatalf("after drain: %s", got)
	}
	if err := a.Undrain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Undrain(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := a.Withdraw(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "define ANNOUNCE = false; define DRAIN = false;" {
		t.Fatalf("after withdraw: %s", got)
	}
	// Timeout is enforced.
	slow := &ExecAnnouncer{Path: "sleep", Timeout: 50 * time.Millisecond}
	if err := slow.run(ctx, "5"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout: %v", err)
	}
	if _, ok := NewAnnouncer("").(NopAnnouncer); !ok {
		t.Fatal("NewAnnouncer(\"\") should be Nop")
	}
	if err := (NopAnnouncer{}).Announce(ctx); err != nil {
		t.Fatal(err)
	}
}
