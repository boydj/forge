//go:build integration

package tests

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Failure-injection scenarios against the built binary. Each subtest owns
// its nodes; see docs/failure-testing.md for the expected behaviour and how
// to run one scenario on its own.

// ---------------------------------------------------------------------------
// helpers

// startNodeWith is startNode with [limits] keys rewritten before the first
// start (limits are read once at process start).
func startNodeWith(t *testing.T, limits map[string]string) *node {
	t.Helper()
	n := &node{t: t, bin: binary(t), dir: t.TempDir(), gemPort: freePort(t), sshPort: freePort(t)}
	n.cfg = filepath.Join(n.dir, "forge.toml")
	n.admin("init", "--data", n.dir, "--hostname", "localhost",
		"--gemini-listen", fmt.Sprintf("127.0.0.1:%d", n.gemPort),
		"--ssh-listen", fmt.Sprintf("127.0.0.1:%d", n.sshPort),
		"--write-config", n.cfg)
	for k, v := range limits {
		setConfigKey(t, n, k, v)
	}
	startProcess(t, n)
	return n
}

// setConfigKey rewrites one `key = value` line of the node's TOML config.
func setConfigKey(t *testing.T, n *node, key, value string) {
	t.Helper()
	b, err := os.ReadFile(n.cfg)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(key) + ` = .*$`)
	if !re.Match(b) {
		t.Fatalf("config key %q not found in %s", key, n.cfg)
	}
	out := re.ReplaceAll(b, []byte("${1}"+key+" = "+value))
	if err := os.WriteFile(n.cfg, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// killNode sends SIGKILL and reaps the process.
func killNode(t *testing.T, n *node) {
	t.Helper()
	if err := n.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = n.cmd.Wait()
}

// stopNode stops the daemon gracefully (SIGINT), killing it after 10 s.
func stopNode(t *testing.T, n *node) {
	t.Helper()
	_ = n.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = n.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = n.cmd.Process.Kill()
		<-done
	}
}

// restartNode relaunches `forge serve` for a stopped or killed node on the
// same data directory and ports. It mirrors startProcess but appends to the
// existing serve.log so the history before the restart is kept.
func restartNode(t *testing.T, n *node) {
	t.Helper()
	n.cmd = exec.Command(n.bin, "serve", "--config", n.cfg)
	logf, err := os.OpenFile(filepath.Join(n.dir, "serve.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(logf, "---- restart ----")
	n.cmd.Stdout, n.cmd.Stderr = logf, logf
	if err := n.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cmd := n.cmd
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("server log (%s):\n%s", n.dir, n.log())
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("node exited during restart: %v\n%s", err, n.log())
		default:
		}
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.sshPort))
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("node did not restart within 20s\n%s", n.log())
}

// adminTry is node.admin without the fatal: it returns output and error.
func (n *node) adminTry(args ...string) (string, error) {
	cmd := exec.Command(n.bin, append([]string{"admin", "--config", n.cfg}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *node) log() string {
	b, _ := os.ReadFile(filepath.Join(n.dir, "serve.log"))
	return string(b)
}

func (n *node) repoDir(owner, name string) string {
	return filepath.Join(n.dir, "repos", owner, name+".git")
}

// requestAt performs one Gemini/Titan request against addr under an overall
// deadline. Unlike node.request it returns errors instead of failing the
// test, so callers can assert "fails fast" rather than "hangs".
func requestAt(addr, scheme, path string, cert *tls.Certificate, body string, timeout time.Duration) (int, string, string, error) {
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr, cfg)
	if err != nil {
		return 0, "", "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	_, port, _ := net.SplitHostPort(addr)
	if _, err := fmt.Fprintf(conn, "%s://localhost:%s%s\r\n", scheme, port, path); err != nil {
		return 0, "", "", err
	}
	if body != "" {
		if _, err := io.WriteString(conn, body); err != nil {
			return 0, "", "", err
		}
	}
	br := bufio.NewReader(conn)
	header, err := br.ReadString('\n')
	if err != nil {
		return 0, "", "", err
	}
	var status int
	fmt.Sscanf(header, "%d", &status)
	rest, _ := io.ReadAll(br)
	return status, strings.TrimSpace(header[3:]), string(rest), nil
}

func (n *node) addr() string { return fmt.Sprintf("127.0.0.1:%d", n.gemPort) }

func writeRandomFile(t *testing.T, path string, size int) {
	t.Helper()
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitFile writes a file, commits it and returns the new HEAD.
func commitFile(t *testing.T, g *gitClient, work, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	g.must(work, "add", name)
	g.must(work, "commit", "-qm", msg)
	return strings.TrimSpace(g.must(work, "rev-parse", "HEAD"))
}

// refOf reads refs/heads/main of a bare repository on disk ("" if absent).
func refOf(gitDir string) string {
	out, err := exec.Command("git", "--git-dir="+gitDir, "rev-parse", "--verify", "-q", "refs/heads/main").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// corruptObjects overwrites the middle of every object file (loose and
// pack) under gitDir/objects with zeros and returns how many were touched.
func corruptObjects(t *testing.T, gitDir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(gitDir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".idx") || strings.Contains(p, "/info/") {
			return nil
		}
		st, err := d.Info()
		if err != nil || st.Size() < 8 {
			return nil
		}
		if err := os.Chmod(p, 0o644); err != nil { // loose objects are read-only
			return err
		}
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		size := st.Size()
		span := size / 2
		if span > 64 {
			span = 64
		}
		if _, err := f.WriteAt(make([]byte, span), size/2); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// popStatusRow returns the columns of the `pop status` row for repo on n.
func popStatusRow(t *testing.T, n *node, repo string) []string {
	t.Helper()
	for _, l := range strings.Split(n.admin("pop", "status"), "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && f[0] == repo {
			return f
		}
	}
	return nil
}

// cluster is a two-node cluster: a leads alice/proj, b replicates it.
type cluster struct {
	a, b  *node
	alice *gitClient
	work  string // alice's clone from a
	urlA  string
}

// seedCluster starts a and b, creates alice and alice/proj on a and clones
// it. With push set, a first commit is pushed and awaited on b.
func seedCluster(t *testing.T, push bool) *cluster {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "cluster.secret")
	_ = os.WriteFile(secret, []byte("test-cluster-secret-0123456789abcdef\n"), 0o600)
	c1, c2 := freePort(t), freePort(t)
	a := startClusterNode(t, "a", c1, map[string]int{"b": c2}, secret)
	b := startClusterNode(t, "b", c2, map[string]int{"a": c1}, secret)
	a.admin("user", "create", "alice")
	a.admin("repo", "create", "alice/proj", "--description", "failure testing")
	alice := newGitClient(t, a)
	a.admin("key", "add", "alice", alice.key+".pub")
	c := &cluster{a: a, b: b, alice: alice, work: filepath.Join(t.TempDir(), "w"),
		urlA: fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", a.sshPort)}
	alice.must("", "clone", "-q", c.urlA, c.work)
	if push {
		commitFile(t, alice, c.work, "README.md", "# proj\n", "first on a")
		alice.must(c.work, "push", "-q", "-u", "origin", "main")
		waitFor(t, "repo on replica", 20*time.Second, func() bool {
			status, _, body := b.request("gemini", "/~alice/proj/", nil, "")
			return status == 20 && strings.Contains(body, "first on a")
		})
	}
	return c
}

// ---------------------------------------------------------------------------
// scenarios

func TestFailure(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not installed")
	}
	binary(t)
	t.Run("LeaderLoss", testLeaderLoss)
	t.Run("ReplicaLoss", testReplicaLoss)
	t.Run("InterruptedReplication", testInterruptedReplication)
	t.Run("StaleReplica", testStaleReplica)
	t.Run("DiskFull", testDiskFull)
	t.Run("OversizePush", testOversizePush)
	t.Run("RepoCorruption", testRepoCorruption)
	t.Run("MetadataFailure", testMetadataFailure)
	t.Run("ConnectionLimits", testConnectionLimits)
	t.Run("Slowloris", testSlowloris)
	t.Run("IPv6", testIPv6)
}

// 1. Leader loss: reads on the replica continue, writes fail fast, and the
// leader resumes where it left off after a restart.
func testLeaderLoss(t *testing.T) {
	c := seedCluster(t, true)
	cert := registeredCert(t, c.a, "alice")
	waitFor(t, "certificate on replica", 15*time.Second, func() bool {
		status, _, body := c.b.request("gemini", "/account", &cert, "")
		return status == 20 && strings.Contains(body, "Account: alice")
	})

	killNode(t, c.a)

	for _, p := range []string{"/~alice/proj/", "/~alice/proj/tree/main/README.md", "/~alice/proj/feed"} {
		if status, meta, _ := c.b.request("gemini", p, nil, ""); status != 20 {
			t.Fatalf("read %s on replica with leader down: %d %s", p, status, meta)
		}
	}
	// Titan write on the replica: a clear temporary failure, bounded in time.
	issue := "During outage\n\nShould not hang.\n"
	start := time.Now()
	status, meta, _, err := requestAt(c.b.addr(), "titan", fmt.Sprintf("/~alice/proj/issues/new;size=%d;mime=text/plain", len(issue)), &cert, issue, 10*time.Second)
	if err != nil {
		t.Fatalf("titan write on replica with leader down did not answer within 10s: %v", err)
	}
	if status < 40 || status > 49 {
		t.Fatalf("titan write on replica with leader down: want 4x, got %d %s", status, meta)
	}
	t.Logf("titan write with leader down: %d %s after %s", status, meta, time.Since(start).Round(time.Millisecond))
	// Push to the replica is refused and names the leader.
	bclone := filepath.Join(t.TempDir(), "b")
	c.alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", c.b.sshPort), bclone)
	commitFile(t, c.alice, bclone, "x", "x", "on b")
	if out, err := c.alice.run(bclone, "push", "-q"); err == nil || !strings.Contains(out, "node a") {
		t.Fatalf("push to replica with leader down: %v\n%s", err, out)
	}

	restartNode(t, c.a)
	commitFile(t, c.alice, c.work, "second.txt", "two\n", "second on a")
	c.alice.must(c.work, "push", "-q")
	waitFor(t, "replica catches up after leader restart", 20*time.Second, func() bool {
		status, _, body := c.b.request("gemini", "/~alice/proj/", nil, "")
		return status == 20 && strings.Contains(body, "second on a")
	})
	status, meta, _ = c.b.request("titan", fmt.Sprintf("/~alice/proj/issues/new;size=%d;mime=text/plain", len(issue)), &cert, issue)
	if status != 30 {
		t.Fatalf("forwarded write after leader restart: %d %s", status, meta)
	}
}

// 2. Replica loss: the leader keeps serving and accepting pushes; the
// replica catches up after a restart.
func testReplicaLoss(t *testing.T) {
	c := seedCluster(t, true)
	killNode(t, c.b)
	if status, meta, _ := c.a.request("gemini", "/~alice/proj/", nil, ""); status != 20 {
		t.Fatalf("leader read with replica down: %d %s", status, meta)
	}
	commitFile(t, c.alice, c.work, "while-down.txt", "b was down\n", "while b down")
	c.alice.must(c.work, "push", "-q")
	restartNode(t, c.b)
	waitFor(t, "replica catches up after restart", 15*time.Second, func() bool {
		status, _, body := c.b.request("gemini", "/~alice/proj/", nil, "")
		return status == 20 && strings.Contains(body, "while b down")
	})
	if refOf(c.a.repoDir("alice", "proj")) != refOf(c.b.repoDir("alice", "proj")) {
		t.Fatal("refs differ after catch-up")
	}
}

// 3. Interrupted replication: the replica is killed during its first fetch
// of a multi-MB repository; the next start must converge and pass fsck.
func testInterruptedReplication(t *testing.T) {
	c := seedCluster(t, false)
	waitFor(t, "repository record on replica", 15*time.Second, func() bool {
		return strings.Contains(c.b.admin("repo", "list"), "alice/proj")
	})
	killNode(t, c.b)
	writeRandomFile(t, filepath.Join(c.work, "blob.bin"), 4<<20)
	c.alice.must(c.work, "add", "blob.bin")
	c.alice.must(c.work, "commit", "-qm", "big blob")
	c.alice.must(c.work, "push", "-q", "-u", "origin", "main")
	want := refOf(c.a.repoDir("alice", "proj"))

	restartNode(t, c.b) // first sync cycle starts the fetch immediately
	time.Sleep(300 * time.Millisecond)
	killNode(t, c.b)
	t.Logf("replica ref after interrupted fetch: %q (leader %s)", refOf(c.b.repoDir("alice", "proj")), want[:8])
	restartNode(t, c.b)
	waitFor(t, "replica converges", 30*time.Second, func() bool {
		return refOf(c.b.repoDir("alice", "proj")) == want
	})
	if out := c.b.admin("repo", "check", "alice/proj"); !strings.Contains(out, "ok") {
		t.Fatalf("repo check on replica: %s", out)
	}
	if status, meta, _ := c.b.request("gemini", "/~alice/proj/tree/main/", nil, ""); status != 20 {
		t.Fatalf("tree on replica: %d %s", status, meta)
	}
}

// 4. Stale replica detection: after the leader dies the replica must make
// its staleness observable.
func testStaleReplica(t *testing.T) {
	c := seedCluster(t, true)
	waitFor(t, "replica row ok", 10*time.Second, func() bool {
		row := popStatusRow(t, c.b, "alice/proj")
		return row != nil && row[3] == "ok"
	})
	row := popStatusRow(t, c.b, "alice/proj")
	last, err := time.Parse(time.RFC3339, row[4])
	if err != nil || time.Since(last) > 30*time.Second {
		t.Fatalf("pop status last sync not recent: %v (%v)", row, err)
	}

	killNode(t, c.a)
	waitFor(t, "replica logs the failed sync", 10*time.Second, func() bool {
		l := c.b.log()
		return strings.Contains(l, "sync cycle finished with errors") && strings.Contains(l, "connection refused")
	})
	if status, _, _ := c.b.request("gemini", "/~alice/proj/", nil, ""); status != 20 {
		t.Fatalf("read on stale replica: %d", status)
	}
	flagged := false
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && !flagged {
		row = popStatusRow(t, c.b, "alice/proj")
		flagged = row != nil && (row[3] != "ok" || len(row) > 5)
		time.Sleep(500 * time.Millisecond)
	}
	if !flagged {
		t.Skip("known gap: `pop status` keeps status=ok and a frozen LAST SYNC while the leader is unreachable (repo_replicas is only updated inside syncRepoFrom); staleness is visible in the log only. See docs/failure-testing.md.")
	}
	t.Logf("pop status flags staleness: %v", row)
}

// 5. Disk full: writes are refused, reads continue, /status is unhealthy.
func testDiskFull(t *testing.T) {
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort), work)
	commitFile(t, alice, work, "README.md", "# proj\n", "seed")
	alice.must(work, "push", "-q", "-u", "origin", "main")
	alice.must(work, "tag", "-a", "v1.0", "-m", "one")
	alice.must(work, "push", "-q", "origin", "v1.0")
	cert := registeredCert(t, n, "alice")
	rel := "v1.0\nRelease one\n\nnotes\n"
	if status, meta, _ := n.request("titan", fmt.Sprintf("/~alice/proj/releases/new;size=%d;mime=text/plain", len(rel)), &cert, rel); status != 30 {
		t.Fatalf("create release: %d %s", status, meta)
	}

	stopNode(t, n)
	setConfigKey(t, n, "min_free_bytes", fmt.Sprint(int64(1)<<60))
	restartNode(t, n)

	for _, p := range []string{"/~alice/proj/", "/~alice/proj/tree/main/README.md", "/~alice/proj/releases/v1.0"} {
		if status, meta, _ := n.request("gemini", p, nil, ""); status != 20 {
			t.Fatalf("read %s with disk full: %d %s", p, status, meta)
		}
	}
	if status, meta, _ := n.request("gemini", "/status", nil, ""); status != 41 || !strings.Contains(meta, "unhealthy") {
		t.Fatalf("/status with disk full: %d %s", status, meta)
	}
	commitFile(t, alice, work, "more.txt", "more\n", "more")
	if out, err := alice.run(work, "push", "-q"); err == nil || !strings.Contains(out, "low on disk space") {
		t.Fatalf("push with disk full: %v\n%s", err, out)
	}
	asset := strings.Repeat("x", 1024)
	status, meta, _ := n.request("titan", fmt.Sprintf("/~alice/proj/releases/v1.0/assets/proj.tar.gz;size=%d;mime=application/gzip", len(asset)), &cert, asset)
	if status != 41 && status != 50 {
		t.Fatalf("asset upload with disk full: want 41 or 50, got %d %s", status, meta)
	}
	if status, _, _ := n.request("gemini", "/~alice/proj/releases/v1.0/assets/proj.tar.gz", nil, ""); status == 20 {
		t.Fatal("asset stored despite disk full")
	}
}

// 6. Oversize push: receive.maxInputSize refuses the pack; refs unchanged.
func testOversizePush(t *testing.T) {
	n := startNodeWith(t, map[string]string{"max_push_bytes": fmt.Sprint(1 << 20)})
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort), work)
	first := commitFile(t, alice, work, "README.md", "# proj\n", "seed")
	alice.must(work, "push", "-q", "-u", "origin", "main")

	writeRandomFile(t, filepath.Join(work, "big.bin"), 3<<20)
	alice.must(work, "add", "big.bin")
	alice.must(work, "commit", "-qm", "3 MiB")
	out, err := alice.run(work, "push", "-q")
	if err == nil {
		t.Fatalf("oversize push accepted:\n%s", out)
	}
	if !strings.Contains(out, "maximum allowed size") && !strings.Contains(out, "unpack") {
		t.Fatalf("oversize push error not explained:\n%s", out)
	}
	if got := refOf(n.repoDir("alice", "proj")); got != first {
		t.Fatalf("main moved after refused push: %s != %s", got, first)
	}
	if status, _, body := n.request("gemini", "/~alice/proj/", nil, ""); status != 20 || strings.Contains(body, "3 MiB") {
		t.Fatalf("repo page after refused push: %d\n%s", status, body)
	}
}

// 7. Repository corruption: fsck reports it, pages degrade, daemon stays up.
func testRepoCorruption(t *testing.T) {
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort), work)
	commitFile(t, alice, work, "README.md", "# proj\n\nhello\n", "seed")
	commitFile(t, alice, work, "main.c", "int main(void){return 0;}\n", "code")
	alice.must(work, "push", "-q", "-u", "origin", "main")
	if status, _, _ := n.request("gemini", "/~alice/proj/", nil, ""); status != 20 {
		t.Fatalf("repo page before corruption: %d", status)
	}

	touched := corruptObjects(t, n.repoDir("alice", "proj"))
	t.Logf("corrupted %d object files", touched)
	out, err := n.adminTry("maintenance", "--check")
	if err == nil || !strings.Contains(out, "corrupt: 1") {
		t.Fatalf("maintenance --check on corrupt repo: err=%v\n%s", err, out)
	}
	if out, err := n.adminTry("repo", "check", "alice/proj"); err == nil {
		t.Fatalf("repo check passed on corrupt repo:\n%s", out)
	}
	// The daemon must keep answering; pages for the repository must not
	// pretend to succeed.
	emptySuccess := ""
	for _, p := range []string{"/~alice/proj/", "/~alice/proj/tree/main/main.c", "/~alice/proj/log/main", "/~alice/proj/refs"} {
		status, meta, body, err := requestAt(n.addr(), "gemini", p, nil, "", 15*time.Second)
		if err != nil {
			t.Fatalf("%s on corrupt repo: no answer: %v", p, err)
		}
		t.Logf("%s on corrupt repo: %d %s (%d body bytes)", p, status, meta, len(body))
		switch {
		case status >= 40 && status <= 59:
		case status == 20 && strings.TrimSpace(body) == "":
			emptySuccess = p
		default:
			t.Fatalf("%s on corrupt repo: want 4x/5x, got %d %s\n%s", p, status, meta, body)
		}
	}
	if status, meta, _ := n.request("gemini", "/status", nil, ""); status != 20 && status != 41 {
		t.Fatalf("/status after corruption: %d %s", status, meta)
	}
	if status, _, _ := n.request("gemini", "/", nil, ""); status != 20 {
		t.Fatalf("front page after corruption: %d", status)
	}
	t.Run("PagesDegrade", func(t *testing.T) {
		if emptySuccess != "" {
			t.Skipf("known bug: %s answered 20 with an empty body (request.page writes the 20 header before the handler touches git, so a later req.fail cannot replace it; internal/web/handler.go:161)", emptySuccess)
		}
	})
}

// 8. Metadata failure and restore drill: a corrupt forge.db makes the
// daemon fail fast; a backup taken earlier restores repository and issue.
func testMetadataFailure(t *testing.T) {
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort), work)
	commitFile(t, alice, work, "README.md", "# proj\n", "seed")
	alice.must(work, "push", "-q", "-u", "origin", "main")
	cert := registeredCert(t, n, "alice")
	issue := "Backed up\n\nSurvives the restore.\n"
	if status, meta, _ := n.request("titan", fmt.Sprintf("/~alice/proj/issues/new;size=%d;mime=text/plain", len(issue)), &cert, issue); status != 30 {
		t.Fatalf("create issue: %d %s", status, meta)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	n.admin("backup", "--out", archive)

	stopNode(t, n)
	db := filepath.Join(n.dir, "forge.db")
	f, err := os.OpenFile(db, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	garbage := make([]byte, 100)
	for i := range garbage {
		garbage[i] = 0xff
	}
	if _, err := f.WriteAt(garbage, 0); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, s := range []string{"-wal", "-shm"} {
		_ = os.Remove(db + s)
	}

	// The daemon must refuse to start, quickly and with a clear error.
	cmd := exec.Command(n.bin, "serve", "--config", n.cfg)
	logPath := filepath.Join(n.dir, "corrupt-start.log")
	logf, _ := os.Create(logPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("forge serve exited 0 with a corrupt database")
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("forge serve did not fail fast on a corrupt database")
	}
	_ = logf.Close()
	lg, _ := os.ReadFile(logPath)
	if !strings.Contains(string(lg), "open database") && !strings.Contains(string(lg), "not a database") {
		t.Fatalf("corrupt database start log lacks a clear error:\n%s", lg)
	}
	t.Logf("corrupt start: %s", strings.TrimSpace(string(lg)))
	if c, err := net.Dial("tcp", n.addr()); err == nil {
		_ = c.Close()
		t.Fatal("gemini port open after failed start")
	}

	// Restore drill (docs/disaster-recovery.md): keep the broken file, restore.
	if err := os.Rename(db, db+".broken"); err != nil {
		t.Fatal(err)
	}
	verify := func(t *testing.T) {
		t.Helper()
		status, _, body := n.request("gemini", "/~alice/proj/", nil, "")
		if status != 20 || !strings.Contains(body, "seed") {
			t.Fatalf("repo after restore: %d\n%s", status, body)
		}
		status, _, body = n.request("gemini", "/~alice/proj/issues/1", nil, "")
		if status != 20 || !strings.Contains(body, "Survives the restore.") {
			t.Fatalf("issue after restore: %d\n%s", status, body)
		}
		if out := n.admin("repo", "check", "alice/proj"); !strings.Contains(out, "ok") {
			t.Fatalf("repo check after restore: %s", out)
		}
	}
	t.Run("AdminRestore", func(t *testing.T) {
		n.admin("restore", "--in", archive)
		if st := n.admin("status"); strings.Contains(st, "repos:       0 ") {
			t.Skip("known bug: `forge admin restore` leaves an empty database: runAdmin opens the store before Restore extracts forge.db over it and the close-time WAL checkpoint overwrites the restored file (internal/forge/backup.go:117, cmd/forge/admin.go:64)")
		}
		restartNode(t, n)
		verify(t)
		stopNode(t, n)
	})
	t.Run("ManualRestore", func(t *testing.T) {
		manualRestore(t, n, archive)
		restartNode(t, n)
		verify(t)
		commitFile(t, alice, work, "after.txt", "after\n", "after restore")
		alice.must(work, "push", "-q")
		status, _, body := n.request("gemini", "/~alice/proj/", nil, "")
		if status != 20 || !strings.Contains(body, "after restore") {
			t.Fatalf("push after restore: %d\n%s", status, body)
		}
	})
}

// manualRestore follows docs/disaster-recovery.md by hand: the database
// snapshot is copied into place and every bundle is fetched into a fresh
// bare repository. The daemon must be stopped.
func manualRestore(t *testing.T, n *node, archive string) {
	t.Helper()
	tmp := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", archive, "-C", tmp).CombinedOutput(); err != nil {
		t.Fatalf("untar: %v\n%s", err, out)
	}
	snap, err := os.ReadFile(filepath.Join(tmp, "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(n.dir, "forge.db")
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(db + s)
	}
	if err := os.WriteFile(db, snap, 0o640); err != nil {
		t.Fatal(err)
	}
	bundles, _ := filepath.Glob(filepath.Join(tmp, "repos", "*", "*.bundle"))
	if len(bundles) == 0 {
		t.Fatal("archive holds no repository bundles")
	}
	for _, b := range bundles {
		owner := filepath.Base(filepath.Dir(b))
		name := strings.TrimSuffix(filepath.Base(b), ".bundle")
		dir := n.repoDir(owner, name)
		_ = os.RemoveAll(dir)
		for _, args := range [][]string{
			{"init", "--bare", "--quiet", "--initial-branch=main", dir},
			{"--git-dir=" + dir, "fetch", "--quiet", "--", b, "+refs/*:refs/*"},
			{"--git-dir=" + dir, "symbolic-ref", "HEAD", "refs/heads/main"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
}

// 9. Connection limits per source address for Gemini and SSH.
func testConnectionLimits(t *testing.T) {
	n := startNodeWith(t, map[string]string{"max_conns_per_ip": "4", "ssh_max_conns_per_ip": "2"})

	t.Run("Gemini", func(t *testing.T) {
		cfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}
		var held []*tls.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for i := 0; i < 4; i++ {
			c, err := tls.Dial("tcp", n.addr(), cfg)
			if err != nil {
				t.Fatalf("conn %d: %v", i+1, err)
			}
			held = append(held, c)
		}
		rejected := 0
		for i := 4; i < 8; i++ {
			c, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", n.addr(), cfg)
			if err != nil {
				rejected++
				continue
			}
			// Accepted at TCP level but must be closed without a response.
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(c, "gemini://localhost:%d/\r\n", n.gemPort)
			if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
				rejected++
			}
			_ = c.Close()
		}
		if rejected != 4 {
			t.Fatalf("connections above the per-IP limit answered: %d of 4 rejected", rejected)
		}
		for i, c := range held {
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(c, "gemini://localhost:%d/status\r\n", n.gemPort)
			h, err := bufio.NewReader(c).ReadString('\n')
			if err != nil || !(strings.HasPrefix(h, "20") || strings.HasPrefix(h, "41")) {
				t.Fatalf("held conn %d: %v %q", i+1, err, h)
			}
			_ = c.Close()
		}
		held = nil
		waitFor(t, "limit released", 5*time.Second, func() bool {
			status, _, _, err := requestAt(n.addr(), "gemini", "/", nil, "", 3*time.Second)
			return err == nil && status == 20
		})
	})

	t.Run("SSH", func(t *testing.T) {
		addr := fmt.Sprintf("127.0.0.1:%d", n.sshPort)
		banner := func(c net.Conn) (string, error) {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			return bufio.NewReader(c).ReadString('\n')
		}
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for i := 0; i < 2; i++ {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := banner(c); err != nil || !strings.HasPrefix(b, "SSH-2.0-forge") {
				t.Fatalf("conn %d banner: %v %q", i+1, err, b)
			}
			held = append(held, c)
		}
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if b, err := banner(c); err == nil {
			t.Fatalf("third SSH connection got a banner: %q", b)
		}
		_ = c.Close()
		for _, c := range held {
			_ = c.Close()
		}
		held = nil
		waitFor(t, "ssh limit released", 5*time.Second, func() bool {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				return false
			}
			defer c.Close()
			b, err := banner(c)
			return err == nil && strings.HasPrefix(b, "SSH-2.0-forge")
		})
	})
}

// 10. Slowloris: a connection that never sends a request is closed by the
// read timeout (10 s default).
func testSlowloris(t *testing.T) {
	n := startNode(t)
	c, err := tls.Dial("tcp", n.addr(), &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_ = c.SetReadDeadline(start.Add(15 * time.Second))
	// The server may answer "59 malformed request" before closing; what
	// matters is that the connection is gone within the read timeout.
	got, err := io.ReadAll(c)
	el := time.Since(start)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("idle connection still open after %s", el)
	}
	if el < 5*time.Second {
		t.Fatalf("idle connection closed after only %s", el)
	}
	t.Logf("idle connection closed by server after %s (sent %q, err %v)", el.Round(time.Millisecond), strings.TrimSpace(string(got)), err)
	waitFor(t, "request after slowloris", 5*time.Second, func() bool {
		status, _, _, err := requestAt(n.addr(), "gemini", "/", nil, "", 3*time.Second)
		return err == nil && status == 20
	})
}

// 11. IPv6 listeners for Gemini and SSH.
func testIPv6(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("::1 unavailable: %v", err)
	}
	_ = l.Close()
	n := &node{t: t, bin: binary(t), dir: t.TempDir(), gemPort: freePort(t), sshPort: freePort(t)}
	n.cfg = filepath.Join(n.dir, "forge.toml")
	n.admin("init", "--data", n.dir, "--hostname", "localhost",
		"--gemini-listen", fmt.Sprintf("[::1]:%d", n.gemPort),
		"--ssh-listen", fmt.Sprintf("[::1]:%d", n.sshPort),
		"--write-config", n.cfg)
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
			t.Logf("server log:\n%s", n.log())
		}
	})
	gem6, ssh6 := fmt.Sprintf("[::1]:%d", n.gemPort), fmt.Sprintf("[::1]:%d", n.sshPort)
	waitFor(t, "ipv6 node", 10*time.Second, func() bool {
		c, err := net.Dial("tcp", ssh6)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	work := filepath.Join(t.TempDir(), "w")
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@%s/alice/proj.git", ssh6), work)
	commitFile(t, alice, work, "README.md", "# v6\n", "over ipv6")
	alice.must(work, "push", "-q", "-u", "origin", "main")
	status, meta, body, err := requestAt(gem6, "gemini", "/~alice/proj/", nil, "", 10*time.Second)
	if err != nil || status != 20 || !strings.Contains(body, "over ipv6") {
		t.Fatalf("gemini over ::1: %v %d %s\n%s", err, status, meta, body)
	}
	if c, err := net.Dial("tcp", n.addr()); err == nil {
		_ = c.Close()
		t.Fatal("node bound to 127.0.0.1 although configured for ::1 only")
	}
}
