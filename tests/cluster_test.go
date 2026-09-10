//go:build integration

package tests

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clusterNode extends node with control-plane settings.
func startClusterNode(t *testing.T, name string, control int, peers map[string]int, secret string) *node {
	t.Helper()
	n := &node{t: t, bin: binary(t), dir: t.TempDir(), gemPort: freePort(t), sshPort: freePort(t)}
	n.cfg = filepath.Join(n.dir, "forge.toml")
	n.admin("init", "--data", n.dir, "--hostname", "localhost", "--node", name,
		"--gemini-listen", fmt.Sprintf("127.0.0.1:%d", n.gemPort),
		"--ssh-listen", fmt.Sprintf("127.0.0.1:%d", n.sshPort),
		"--write-config", n.cfg)
	cfg, _ := os.ReadFile(n.cfg)
	s := string(cfg)
	if i := strings.Index(s, "[cluster]"); i >= 0 {
		s = s[:i]
	}
	s += fmt.Sprintf("\n[cluster]\n  enabled = true\n  control_listen = \"127.0.0.1:%d\"\n  secret_file = %q\n  sync_interval = \"500ms\"\n  [cluster.peers]\n", control, secret)
	for p, port := range peers {
		s += fmt.Sprintf("    %s = \"127.0.0.1:%d\"\n", p, port)
	}
	if err := os.WriteFile(n.cfg, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	startProcess(t, n)
	return n
}

// startProcess launches `forge serve` for an initialised node.
func startProcess(t *testing.T, n *node) {
	t.Helper()
	n.cmd = exec.Command(n.bin, "serve", "--config", n.cfg)
	logf, _ := os.Create(filepath.Join(n.dir, "serve.log"))
	n.cmd.Stdout, n.cmd.Stderr = logf, logf
	if err := n.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = n.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = n.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = n.cmd.Process.Kill()
		}
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(n.dir, "serve.log"))
			t.Logf("server log (%s):\n%s", n.dir, b)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.sshPort))
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("node did not start")
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestTwoNodeReplication: a repository pushed to the leader appears on the
// replica; a Titan write at the replica is forwarded to the leader and
// replicated back.
func TestTwoNodeReplication(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not installed")
	}
	secret := filepath.Join(t.TempDir(), "cluster.secret")
	_ = os.WriteFile(secret, []byte("test-cluster-secret-0123456789abcdef\n"), 0o600)
	c1, c2 := freePort(t), freePort(t)
	a := startClusterNode(t, "a", c1, map[string]int{"b": c2}, secret)
	b := startClusterNode(t, "b", c2, map[string]int{"a": c1}, secret)

	a.admin("user", "create", "alice")
	a.admin("repo", "create", "alice/proj", "--description", "replicated")
	alice := newGitClient(t, a)
	a.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", a.sshPort), work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# replicated\n"), 0o644)
	alice.must(work, "add", ".")
	alice.must(work, "commit", "-qm", "first on a")
	alice.must(work, "push", "-q", "-u", "origin", "main")

	waitFor(t, "repo on replica", 20*time.Second, func() bool {
		status, _, body := b.request("gemini", "/~alice/proj/", nil, "")
		return status == 20 && strings.Contains(body, "first on a") && strings.Contains(body, "# replicated")
	})
	waitFor(t, "events on replica", 10*time.Second, func() bool {
		_, _, body := b.request("gemini", "/feed", nil, "")
		return strings.Contains(body, "alice created branch main")
	})
	// Replica refuses pushes and names the leader.
	bclone := filepath.Join(t.TempDir(), "b")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", b.sshPort), bclone)
	_ = os.WriteFile(filepath.Join(bclone, "x"), []byte("x"), 0o644)
	alice.must(bclone, "add", "x")
	alice.must(bclone, "commit", "-qm", "on b")
	if out, err := alice.run(bclone, "push", "-q"); err == nil || !strings.Contains(out, "node a") {
		t.Fatalf("push to replica: %v\n%s", err, out)
	}

	// Register a certificate on the metadata leader (a), wait for it on b,
	// then open an issue through b: it is forwarded to a.
	cert := registeredCert(t, a, "alice")
	waitFor(t, "certificate on replica", 15*time.Second, func() bool {
		status, _, body := b.request("gemini", "/account", &cert, "")
		return status == 20 && strings.Contains(body, "Account: alice")
	})
	issue := "Opened via replica\n\nForwarded to the leader.\n"
	status, meta, _ := b.request("titan", fmt.Sprintf("/~alice/proj/issues/new;size=%d;mime=text/plain", len(issue)), &cert, issue)
	if status != 30 || meta != "/~alice/proj/issues/1" {
		t.Fatalf("forwarded issue: %d %s", status, meta)
	}
	status, _, body := a.request("gemini", "/~alice/proj/issues/1", nil, "")
	if status != 20 || !strings.Contains(body, "Forwarded to the leader.") {
		t.Fatalf("issue on leader: %d\n%s", status, body)
	}
	waitFor(t, "issue on replica", 10*time.Second, func() bool {
		status, _, body := b.request("gemini", "/~alice/proj/issues/1", nil, "")
		return status == 20 && strings.Contains(body, "Forwarded to the leader.")
	})
	if out := b.admin("pop", "status"); !strings.Contains(out, "alice/proj") {
		t.Fatalf("pop status on replica:\n%s", out)
	}
	// Leader move: after sync, b can lead. Run on the old leader.
	out := a.admin("repo", "move-leader", "alice/proj", "b")
	_ = out
	waitFor(t, "b leads", 10*time.Second, func() bool {
		for _, l := range strings.Split(b.admin("repo", "list"), "\n") {
			f := strings.Fields(l)
			if len(f) >= 5 && f[1] == "alice/proj" && f[4] == "b" {
				return true
			}
		}
		return false
	})
	_ = os.WriteFile(filepath.Join(bclone, "y"), []byte("y"), 0o644)
	alice.must(bclone, "add", "y")
	alice.must(bclone, "commit", "-qm", "after move")
	alice.must(bclone, "push", "-q", "--force")
	waitFor(t, "a replicates from new leader", 20*time.Second, func() bool {
		status, _, body := a.request("gemini", "/~alice/proj/", nil, "")
		return status == 20 && strings.Contains(body, "after move")
	})
}
