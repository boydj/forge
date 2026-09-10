//go:build integration

// Package tests holds end-to-end acceptance tests that exercise the built
// forge binary over real Gemini, Titan, SSH and git clients.
package tests

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func binary(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../bin/forge")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("build ./bin/forge first (make build)")
	}
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type node struct {
	t       *testing.T
	bin     string
	dir     string
	cfg     string
	gemPort int
	sshPort int
	cmd     *exec.Cmd
}

func startNode(t *testing.T) *node {
	t.Helper()
	n := &node{t: t, bin: binary(t), dir: t.TempDir(), gemPort: freePort(t), sshPort: freePort(t)}
	n.cfg = filepath.Join(n.dir, "forge.toml")
	n.admin("init", "--data", n.dir, "--hostname", "localhost",
		"--gemini-listen", fmt.Sprintf("127.0.0.1:%d", n.gemPort),
		"--ssh-listen", fmt.Sprintf("127.0.0.1:%d", n.sshPort),
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
			b, _ := os.ReadFile(filepath.Join(n.dir, "serve.log"))
			t.Logf("server log:\n%s", b)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.sshPort))
		if err == nil {
			_ = c.Close()
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("node did not start")
	return nil
}

func (n *node) admin(args ...string) string {
	n.t.Helper()
	full := args
	if args[0] != "init" {
		full = append([]string{"--config", n.cfg}, args...)
	}
	cmd := exec.Command(n.bin, append([]string{"admin"}, full...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		n.t.Fatalf("forge admin %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (n *node) gemini(path string) (int, string, string) {
	n.t.Helper()
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.gemPort), &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err != nil {
		n.t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "gemini://localhost:%d%s\r\n", n.gemPort, path)
	br := bufio.NewReader(conn)
	header, err := br.ReadString('\n')
	if err != nil {
		n.t.Fatal(err)
	}
	var status int
	fmt.Sscanf(header, "%d", &status)
	body, _ := io.ReadAll(br)
	return status, strings.TrimSpace(header[3:]), string(body)
}

type gitClient struct {
	t   *testing.T
	key string
	env []string
}

func newGitClient(t *testing.T, n *node) *gitClient {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	// No -p here: ssh keeps the first -p it sees, which would override the
	// port carried by ssh:// URLs (tests talk to several nodes).
	sshCmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		key, filepath.Join(dir, "known_hosts"))
	return &gitClient{t: t, key: key, env: append(os.Environ(),
		"GIT_SSH_COMMAND="+sshCmd,
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.org",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.org",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")}
}

func (g *gitClient) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = g.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (g *gitClient) must(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.run(dir, args...)
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// TestFirstAcceptance: create repo -> ordinary git clone -> git push -> Gemini browse.
func TestFirstAcceptance(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not installed")
	}
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("user", "create", "bob")
	n.admin("repo", "create", "alice/proj", "--description", "acceptance project")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	bob := newGitClient(t, n)
	n.admin("key", "add", "bob", bob.key+".pub")

	work := filepath.Join(t.TempDir(), "proj")
	url := fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort)
	alice.must("", "clone", "-q", url, work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# proj\n\nPushed over SSH.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "main.c"), []byte("int main(void){return 0;}\n"), 0o644)
	alice.must(work, "add", ".")
	alice.must(work, "commit", "-qm", "first commit")
	alice.must(work, "push", "-q", "-u", "origin", "main")
	alice.must(work, "tag", "-a", "v1.0", "-m", "release one")
	alice.must(work, "push", "-q", "origin", "v1.0")

	status, _, body := n.gemini("/~alice/proj/")
	if status != 20 || !strings.Contains(body, "Pushed over SSH.") || !strings.Contains(body, "first commit") {
		t.Fatalf("overview after push: %d\n%s", status, body)
	}
	status, _, body = n.gemini("/~alice/proj/tree/main/main.c")
	if status != 20 || !strings.Contains(body, "int main(void)") {
		t.Fatalf("blob: %d\n%s", status, body)
	}
	status, _, body = n.gemini("/~alice/proj/refs")
	if status != 20 || !strings.Contains(body, "v1.0") || !strings.Contains(body, "release one") {
		t.Fatalf("refs: %d\n%s", status, body)
	}
	status, _, body = n.gemini("/~alice/proj/feed")
	if status != 20 || !strings.Contains(body, "alice created branch main") || !strings.Contains(body, "alice tagged v1.0") {
		t.Fatalf("feed after push: %d\n%s", status, body)
	}
	status, _, body = n.gemini("/feed")
	if status != 20 || !strings.Contains(body, "alice created branch main in alice/proj") {
		t.Fatalf("global feed: %d\n%s", status, body)
	}

	// Second push records commit count and subject.
	_ = os.WriteFile(filepath.Join(work, "main.c"), []byte("int main(void){return 1;}\n"), 0o644)
	alice.must(work, "commit", "-qam", "return one")
	alice.must(work, "push", "-q")
	status, _, body = n.gemini("/~alice/proj/feed")
	if !strings.Contains(body, "alice pushed 1 commit to main in alice/proj: return one") {
		t.Fatalf("second push feed: %d\n%s", status, body)
	}

	// Bob can clone the public repo but not push.
	bwork := filepath.Join(t.TempDir(), "bob")
	bob.must("", "clone", "-q", url, bwork)
	_ = os.WriteFile(filepath.Join(bwork, "x"), []byte("x"), 0o644)
	bob.must(bwork, "add", "x")
	bob.must(bwork, "commit", "-qm", "bob")
	out, err := bob.run(bwork, "push", "-q")
	if err == nil || !strings.Contains(out, "you can only propose changes") {
		t.Fatalf("bob push should be refused: %v\n%s", err, out)
	}

	// Deleting the default branch is refused by the pre-receive hook.
	out, err = alice.run(work, "push", "origin", ":main")
	if err == nil || !strings.Contains(out, "refusing to delete the default branch") {
		t.Fatalf("delete default branch: %v\n%s", err, out)
	}
	// Non-branch refs are refused.
	out, err = alice.run(work, "push", "origin", "main:refs/evil/x")
	if err == nil || !strings.Contains(out, "not allowed") {
		t.Fatalf("evil ref: %v\n%s", err, out)
	}

	// Private repository: bob cannot see or clone it; alice can.
	n.admin("repo", "create", "alice/secret", "--private")
	if status, _, _ := n.gemini("/~alice/secret/"); status != 51 {
		t.Fatalf("private repo visible anonymously: %d", status)
	}
	out, err = bob.run("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/secret.git", n.sshPort), filepath.Join(t.TempDir(), "s"))
	if err == nil {
		t.Fatalf("bob cloned private repo:\n%s", out)
	}
	alice.must("", "clone", "-q", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/secret.git", n.sshPort), filepath.Join(t.TempDir(), "s2"))

	// Unknown key is rejected; shell is refused.
	stranger := newGitClient(t, n)
	out, err = stranger.run("", "clone", "-q", url, filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatalf("unknown key accepted:\n%s", out)
	}
	sshOut, err := exec.Command("ssh", "-i", alice.key, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "BatchMode=yes", "-p", fmt.Sprint(n.sshPort), "git@127.0.0.1", "id").CombinedOutput()
	if err == nil {
		t.Fatalf("shell command executed:\n%s", sshOut)
	}

	// Admin status and backup.
	if out := n.admin("status"); !strings.Contains(out, "repos:       2") {
		t.Fatalf("status: %s", out)
	}
	n.admin("backup", "--out", filepath.Join(t.TempDir(), "snap.db"))
}

// TestSecondAcceptance: authenticate -> create issue via Titan -> read via
// Gemini -> comment via Titan -> observe activity through the gemfeed.
func TestSecondAcceptance(t *testing.T) {
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("repo", "create", "alice/proj")

	// A fresh client certificate registers as "carol" through the INPUT flow.
	cert := newClientCert(t)
	status, meta, _ := n.request("gemini", "/account", &cert, "")
	if status != 20 {
		t.Fatalf("unknown cert should see registration page: %d %s", status, meta)
	}
	status, meta, _ = n.request("gemini", "/account?carol", &cert, "")
	if status != 30 {
		t.Fatalf("register: %d %s", status, meta)
	}

	issue := "Feed test\n\nOpened over Titan.\n"
	status, meta, _ = n.request("titan", fmt.Sprintf("/~alice/proj/issues/new;size=%d;mime=text/plain", len(issue)), &cert, issue)
	if status != 30 || meta != "/~alice/proj/issues/1" {
		t.Fatalf("create issue: %d %s", status, meta)
	}
	status, _, body := n.request("gemini", "/~alice/proj/issues/1", nil, "")
	if status != 20 || !strings.Contains(body, "Opened over Titan.") || !strings.Contains(body, "opened by carol") {
		t.Fatalf("read issue: %d\n%s", status, body)
	}
	comment := "Seen it too.\n"
	status, meta, _ = n.request("titan", fmt.Sprintf("/~alice/proj/issues/1/comment;size=%d;mime=text/gemini", len(comment)), &cert, comment)
	if status != 30 {
		t.Fatalf("comment: %d %s", status, meta)
	}
	status, _, body = n.request("gemini", "/~alice/proj/issues/feed", nil, "")
	if status != 20 || !strings.Contains(body, "carol opened issue #1: Feed test") || !strings.Contains(body, "carol commented on issue #1") {
		t.Fatalf("issue feed: %d\n%s", status, body)
	}
	status, _, body = n.request("gemini", "/~carol/feed", nil, "")
	if status != 20 || !strings.Contains(body, "carol opened issue #1") {
		t.Fatalf("user feed: %d\n%s", status, body)
	}
	status, _, body = n.request("gemini", "/atom.xml", nil, "")
	if status != 20 || !strings.Contains(body, "<title>carol commented on issue #1: Feed test</title>") {
		t.Fatalf("atom: %d\n%s", status, body)
	}
}

func newClientCert(t *testing.T) tls.Certificate {
	t.Helper()
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "c.key"), filepath.Join(dir, "c.crt")
	out, err := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
		"-keyout", key, "-out", crt, "-days", "30", "-subj", "/CN=carol").CombinedOutput()
	if err != nil {
		t.Skipf("openssl: %v %s", err, out)
	}
	c, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// request performs a Gemini or Titan request with an optional client cert.
func (n *node) request(scheme, path string, cert *tls.Certificate, body string) (int, string, string) {
	n.t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.gemPort), cfg)
	if err != nil {
		n.t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s://localhost:%d%s\r\n", scheme, n.gemPort, path)
	if body != "" {
		_, _ = io.WriteString(conn, body)
	}
	br := bufio.NewReader(conn)
	header, err := br.ReadString('\n')
	if err != nil {
		n.t.Fatal(err)
	}
	var status int
	fmt.Sscanf(header, "%d", &status)
	rest, _ := io.ReadAll(br)
	return status, strings.TrimSpace(header[3:]), string(rest)
}

// TestChangeWorkflow: propose a change by pushing to refs/for/main as a
// reader, review and merge it over Titan, and verify the branch advanced.
func TestChangeWorkflow(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not installed")
	}
	n := startNode(t)
	n.admin("user", "create", "alice")
	n.admin("user", "create", "bob")
	n.admin("repo", "create", "alice/proj")
	alice := newGitClient(t, n)
	n.admin("key", "add", "alice", alice.key+".pub")
	bob := newGitClient(t, n)
	n.admin("key", "add", "bob", bob.key+".pub")
	url := fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/proj.git", n.sshPort)

	// Alice seeds main.
	work := filepath.Join(t.TempDir(), "alice")
	alice.must("", "clone", "-q", url, work)
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	alice.must(work, "add", ".")
	alice.must(work, "commit", "-qm", "seed")
	alice.must(work, "push", "-q", "-u", "origin", "main")

	// Bob (reader) cannot push to main but can propose a change.
	bwork := filepath.Join(t.TempDir(), "bob")
	bob.must("", "clone", "-q", url, bwork)
	_ = os.WriteFile(filepath.Join(bwork, "b.txt"), []byte("two\n"), 0o644)
	bob.must(bwork, "add", ".")
	bob.must(bwork, "commit", "-qm", "Add b.txt\n\nBecause we need it.")
	if out, err := bob.run(bwork, "push", "origin", "HEAD:main"); err == nil || !strings.Contains(out, "only propose changes") {
		t.Fatalf("bob push to main: %v\n%s", err, out)
	}
	out := bob.must(bwork, "push", "origin", "HEAD:refs/for/main")
	if !strings.Contains(out, "created change 1 (v1, 1 commit) targeting main") || !strings.Contains(out, "refs/changes/1/v1") {
		t.Fatalf("propose output:\n%s", out)
	}
	status, _, body := n.request("gemini", "/~alice/proj/changes/1", nil, "")
	if status != 20 || !strings.Contains(body, "# #1 Add b.txt") || !strings.Contains(body, "bob -> main, v1, open") || !strings.Contains(body, "Because we need it.") {
		t.Fatalf("change page: %d\n%s", status, body)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/1/diff", nil, "")
	if status != 20 || !strings.Contains(body, "+two") {
		t.Fatalf("change diff: %d\n%s", status, body)
	}
	// Second version after amending.
	_ = os.WriteFile(filepath.Join(bwork, "b.txt"), []byte("two\nthree\n"), 0o644)
	bob.must(bwork, "commit", "-qa", "--amend", "--no-edit")
	out = bob.must(bwork, "push", "origin", "HEAD:refs/changes/1")
	if !strings.Contains(out, "updated change 1 (v2") {
		t.Fatalf("update output:\n%s", out)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/1/interdiff/1/2", nil, "")
	if status != 20 || !strings.Contains(body, "```range-diff") {
		t.Fatalf("interdiff: %d\n%s", status, body)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/1/patch", nil, "")
	if status != 20 || !strings.HasPrefix(body, "From ") {
		t.Fatalf("patch: %d\n%s", status, body)
	}

	// Alice reviews with an anchor and approves, then merges over Titan.
	aliceCert := registeredCert(t, n, "alice")
	review := "approve\n\nLooks fine.\n\n@ b.txt:2\n> three\nGood addition.\n"
	status, meta, _ := n.request("titan", fmt.Sprintf("/~alice/proj/changes/1/review;size=%d;mime=text/plain", len(review)), &aliceCert, review)
	if status != 30 {
		t.Fatalf("review: %d %s", status, meta)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/1", nil, "")
	if !strings.Contains(body, "v2, approved") || !strings.Contains(body, "=> /~alice/proj/changes/1/v2/diff/b.txt b.txt:2 (v2)") || !strings.Contains(body, "> three") {
		t.Fatalf("after review:\n%s", body)
	}
	status, meta, _ = n.request("titan", "/~alice/proj/changes/1/merge;size=0", &aliceCert, "")
	if status != 30 || !strings.HasPrefix(meta, "/~alice/proj/commit/") {
		t.Fatalf("merge: %d %s", status, meta)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/1", nil, "")
	if !strings.Contains(body, "v2, merged") {
		t.Fatalf("after merge:\n%s", body)
	}
	alice.must(work, "pull", "-q")
	if data, _ := os.ReadFile(filepath.Join(work, "b.txt")); string(data) != "two\nthree\n" {
		t.Fatalf("main after merge: %q", data)
	}
	// Pushing to a merged change is refused.
	if out, err := bob.run(bwork, "push", "origin", "HEAD:refs/changes/1"); err == nil || !strings.Contains(out, "is merged") {
		t.Fatalf("push to merged change: %v\n%s", err, out)
	}
	status, _, body = n.request("gemini", "/~alice/proj/changes/feed", nil, "")
	if status != 20 || !strings.Contains(body, "bob proposed change #1") || !strings.Contains(body, "alice merged change #1") {
		t.Fatalf("changes feed:\n%s", body)
	}
}

// registeredCert creates a client certificate and binds it to an existing
// user through the admin enrolment code flow.
func registeredCert(t *testing.T, n *node, user string) tls.Certificate {
	t.Helper()
	cert := newClientCert(t)
	out := n.admin("cert", "enrol-code", user)
	f := strings.Fields(out)
	code := f[len(f)-1]
	status, meta, _ := n.request("gemini", "/account/enrol?"+code, &cert, "")
	if status != 30 {
		t.Fatalf("enrol %s: %d %s", user, status, meta)
	}
	return cert
}
