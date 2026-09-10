//go:build integration

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// loadSummary mirrors tools/loadtest Summary.
type loadSummary struct {
	Mode        string         `json:"mode"`
	Requests    int            `json:"requests"`
	RPS         float64        `json:"rps"`
	DurationS   float64        `json:"duration_s"`
	Latency     map[string]any `json:"latency_ms"`
	Statuses    map[string]int `json:"statuses"`
	Errors      map[string]int `json:"errors"`
	Bytes       int64          `json:"bytes"`
	Concurrency int            `json:"concurrency"`
}

func (s loadSummary) ms(k string) float64 {
	v, _ := s.Latency[k].(float64)
	return v
}

// buildLoadTool compiles tools/loadtest into a temp dir.
func buildLoadTool(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "loadtest")
	cmd := exec.Command(goBin, "build", "-o", out, "./tools/loadtest")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build loadtest: %v\n%s", err, b)
	}
	return out
}

func runLoad(t *testing.T, tool string, env []string, args ...string) loadSummary {
	t.Helper()
	cmd := exec.Command(tool, append(args, "-json")...)
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	// Exit 3 means "errors were recorded"; the summary still parses.
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
			t.Fatalf("loadtest %v: %v\n%s", args, err, stderr.String())
		}
	}
	var s loadSummary
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatalf("loadtest output: %v\n%s", err, out)
	}
	t.Logf("%s: %d requests in %.1fs, %.0f req/s, p50 %.1f ms, p95 %.1f ms, p99 %.1f ms, max %.1f ms, statuses %v, errors %v",
		s.Mode, s.Requests, s.DurationS, s.RPS, s.ms("P50"), s.ms("P95"), s.ms("P99"), s.ms("Max"), s.Statuses, s.Errors)
	return s
}

// TestLoadSmoke runs a short load against a seeded node: 32 concurrent
// Gemini clients for 5 s must see no 4x/5x and a p99 under 500 ms, and a
// small concurrent clone loop must succeed.
func TestLoadSmoke(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not installed")
	}
	tool := buildLoadTool(t)
	// 32 clients reconnecting at full speed race the default per-IP limit
	// of 32 (release happens after the client sees the close), so raise it.
	n := startNodeWith(t, map[string]string{"max_conns_per_ip": "256"})
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj", "--description", "load")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	url := fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort)
	alice.must("", "clone", "-q", url, work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# load\n\n"+strings.Repeat("Some readme text.\n", 50)), 0o644)
	for i := 0; i < 5; i++ {
		commitFile(t, alice, work, fmt.Sprintf("f%d.txt", i), strings.Repeat("x", 2000)+"\n", "commit "+strconv.Itoa(i))
	}
	alice.must(work, "add", ".")
	alice.must(work, "commit", "-qm", "readme")
	alice.must(work, "push", "-q", "-u", "origin", "main")

	// Assertion target: the tree page (one git subprocess per request).
	target := fmt.Sprintf("gemini://localhost:%d/~alice/proj/tree/main/", n.gemPort)
	s := runLoad(t, tool, os.Environ(), "-url", target, "-c", "32", "-d", "5s")
	if s.Requests == 0 {
		t.Fatal("no requests completed")
	}
	for k, v := range s.Statuses {
		if c, _ := strconv.Atoi(k); c >= 40 && v > 0 {
			t.Errorf("status %s seen %d times", k, v)
		}
	}
	if len(s.Errors) > 0 {
		t.Errorf("transport errors: %v", s.Errors)
	}
	if p99 := s.ms("P99"); p99 >= 500 {
		t.Errorf("p99 %.1f ms >= 500 ms", p99)
	}

	// The overview (git log + tree + README) is the heaviest read page;
	// reported for the record, bounded only by "no errors" because it is
	// gated by git.max_concurrent (16) and scales with core count.
	o := runLoad(t, tool, os.Environ(), "-url", fmt.Sprintf("gemini://localhost:%d/~alice/proj/", n.gemPort), "-c", "32", "-d", "3s")
	if len(o.Errors) > 0 {
		t.Errorf("overview transport errors: %v", o.Errors)
	}
	for k, v := range o.Statuses {
		if c, _ := strconv.Atoi(k); c >= 40 && v > 0 {
			t.Errorf("overview status %s seen %d times", k, v)
		}
	}

	c := runLoad(t, tool, alice.env, "-clone", url, "-c", "4", "-n", "8", "-timeout", "60s")
	if c.Statuses["ok"] != 8 || len(c.Errors) > 0 {
		t.Errorf("clone loop: %v %v", c.Statuses, c.Errors)
	}
}
