package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/vcs/git"
)

// --- test doubles -----------------------------------------------------

type testAuth struct {
	mu   sync.Mutex
	keys map[string]*Account // by SHA256 fingerprint
}

func (a *testAuth) add(pub ssh.PublicKey, acct *Account) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys == nil {
		a.keys = map[string]*Account{}
	}
	a.keys[ssh.FingerprintSHA256(pub)] = acct
}

func (a *testAuth) AuthenticateKey(_ context.Context, key ssh.PublicKey) (*Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if acct, ok := a.keys[ssh.FingerprintSHA256(key)]; ok {
		return acct, nil
	}
	return nil, ErrUnknownKey
}

// testAuthz knows alice/proj (alice writes, everyone reads) and
// alice/readonly (nobody writes). Everything else does not exist. Writes to
// a repository in leaders are answered with NotLeaderError for that node.
type testAuthz struct {
	repos   map[string]string // owner/repo -> disk path
	leaders map[string]string // owner/repo -> leader node name
}

func (z *testAuthz) Authorize(_ context.Context, acct *Account, owner, repo string, op Op) (string, error) {
	p, ok := z.repos[owner+"/"+repo]
	if !ok {
		return "", ErrNoRepo
	}
	if op == OpWrite {
		if leader, ok := z.leaders[owner+"/"+repo]; ok {
			return "", &NotLeaderError{Leader: leader}
		}
		if repo == "readonly" || acct.Name != owner {
			return "", ErrForbidden
		}
	}
	return p, nil
}

type testMetrics struct {
	mu       sync.Mutex
	sessions []string
	conns    int
}

func (m *testMetrics) ObserveSession(op string, success bool, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = append(m.sessions, fmt.Sprintf("%s:%v", op, success))
}

func (m *testMetrics) ConnectionsChanged(delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns += delta
}

func (m *testMetrics) snapshot() ([]string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sessions...), m.conns
}

// syncBuffer is a goroutine-safe log sink: server goroutines keep logging
// (connection teardown) after a client command has returned.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- fixtures ---------------------------------------------------------

func gitEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.org",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.org",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// makeFixture creates a bare repo with a small history (copied from
// internal/vcs/git tests) at dir/<name>.git and returns its path.
func makeFixture(t *testing.T, b *git.Backend, dir, name string) string {
	t.Helper()
	ctx := context.Background()
	bare := filepath.Join(dir, "repos", "alice", name+".git")
	if err := b.Init(ctx, bare, "main"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work-"+name)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = gitEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	_ = os.MkdirAll(work, 0o755)
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# proj\n\nhello\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "initial commit")
	run("tag", "v1.0")
	run("push", "-q", "--all", bare)
	run("push", "-q", "--tags", bare)
	return bare
}

type testEnv struct {
	t       *testing.T
	srv     *Server
	addr    *net.TCPAddr
	dir     string
	auth    *testAuth
	metrics *testMetrics
	keyPath string // alice's private key (OpenSSH format)
	logs    *syncBuffer
}

func writeClientKey(t *testing.T, path string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// newTestEnv starts a server with alice's key registered. opts run on the
// server before it serves (settings, a Forwarder, a different Authz).
func newTestEnv(t *testing.T, opts ...func(*Server)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	b, err := git.New(git.Options{HomeDir: dir, HooksDir: filepath.Join(dir, "hooks"), MaxConcurrent: 4})
	if err != nil {
		t.Skip("git not available:", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "hooks"), 0o755)
	proj := makeFixture(t, b, dir, "proj")
	ro := makeFixture(t, b, dir, "readonly")

	hostKey, err := LoadOrCreateHostKey(filepath.Join(dir, "ssh", "host_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	auth := &testAuth{}
	keyPath := filepath.Join(dir, "alice_ed25519")
	auth.add(writeClientKey(t, keyPath), &Account{ID: 7, Name: "alice"})

	logs := &syncBuffer{}
	metrics := &testMetrics{}
	srv := &Server{
		Config: Config{
			MaxAuthTries:     3,
			HandshakeTimeout: 10 * time.Second,
			SessionTimeout:   60 * time.Second,
			IdleTimeout:      30 * time.Second,
			MaxConns:         16,
			MaxConnsPerIP:    16,
			HookSocket:       filepath.Join(dir, "hook.sock"),
		},
		Auth:    auth,
		Authz:   &testAuthz{repos: map[string]string{"alice/proj": proj, "alice/readonly": ro}},
		Git:     b,
		HostKey: hostKey,
		Logger:  slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Metrics: metrics,
	}
	for _, o := range opts {
		o(srv)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := srv.Serve(l); err != nil {
			t.Error("Serve:", err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Error("Shutdown:", err)
		}
		if _, conns := metrics.snapshot(); conns != 0 {
			t.Errorf("connection gauge not balanced: %d", conns)
		}
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})
	return &testEnv{t: t, srv: srv, addr: l.Addr().(*net.TCPAddr), dir: dir, auth: auth,
		metrics: metrics, keyPath: keyPath, logs: logs}
}

func (e *testEnv) port() string { return strconv.Itoa(e.addr.Port) }

func (e *testEnv) sshOpts(keyPath string) []string {
	return []string{
		"-F", "/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-i", keyPath,
	}
}

// sshCommand returns the GIT_SSH_COMMAND value for keyPath.
func (e *testEnv) sshCommand(keyPath string) string {
	return "ssh " + strings.Join(e.sshOpts(keyPath), " ")
}

func (e *testEnv) url(repo string) string {
	return fmt.Sprintf("ssh://git@127.0.0.1:%s/alice/%s.git", e.port(), repo)
}

// git runs git in dir with the test environment and returns combined
// output (stdout + stderr) and the error.
func (e *testEnv) git(dir, keyPath string, extraEnv []string, args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(e.dir), "GIT_SSH_COMMAND="+e.sshCommand(keyPath))
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ssh runs the ssh binary against the server with a raw command.
func (e *testEnv) ssh(keyPath string, extra []string, command ...string) (string, int, error) {
	e.t.Helper()
	args := append(e.sshOpts(keyPath), "-p", e.port())
	args = append(args, extra...)
	args = append(args, "git@127.0.0.1")
	args = append(args, command...)
	cmd := exec.Command("ssh", args...)
	cmd.Env = gitEnv(e.dir)
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return string(out), code, err
}

func requireSSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh binary not available")
	}
}

// --- tests through the real ssh and git binaries ------------------------

func TestCloneProtocolV2(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	dst := filepath.Join(e.dir, "clone-v2")
	out, err := e.git(e.dir, e.keyPath, []string{"GIT_TRACE_PACKET=1"},
		"-c", "protocol.version=2", "clone", "-q", e.url("proj"), dst)
	if err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if !strings.Contains(out, "version 2") {
		t.Errorf("protocol v2 not negotiated:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err != nil {
		t.Error("clone missing README.md")
	}
	if !strings.Contains(e.logs.String(), "protocol=v2") {
		t.Errorf("server did not record v2 session:\n%s", e.logs.String())
	}
	sessions, _ := e.metrics.snapshot()
	if len(sessions) != 1 || sessions[0] != "upload:true" {
		t.Errorf("metrics: %v", sessions)
	}
}

func TestCloneProtocolV0(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	dst := filepath.Join(e.dir, "clone-v0")
	out, err := e.git(e.dir, e.keyPath, []string{"GIT_TRACE_PACKET=1"},
		"-c", "protocol.version=0", "clone", "-q", e.url("proj"), dst)
	if err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if strings.Contains(out, "version 2") {
		t.Errorf("v2 negotiated although client asked for v0:\n%s", out)
	}
}

func TestPushAndFetch(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	a := filepath.Join(e.dir, "clone-a")
	if out, err := e.git(e.dir, e.keyPath, nil, "clone", "-q", e.url("proj"), a); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	_ = os.WriteFile(filepath.Join(a, "new.txt"), []byte("pushed\n"), 0o644)
	if out, err := e.git(a, e.keyPath, nil, "add", "new.txt"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := e.git(a, e.keyPath, nil, "commit", "-q", "-m", "add new.txt"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := e.git(a, e.keyPath, nil, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	want, _ := e.git(a, e.keyPath, nil, "rev-parse", "HEAD")

	b := filepath.Join(e.dir, "clone-b")
	if out, err := e.git(e.dir, e.keyPath, nil, "clone", "-q", e.url("proj"), b); err != nil {
		t.Fatalf("second clone: %v\n%s", err, out)
	}
	got, _ := e.git(b, e.keyPath, nil, "rev-parse", "HEAD")
	if got != want {
		t.Errorf("fetched HEAD %q want %q", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(b, "new.txt")); err != nil || string(data) != "pushed\n" {
		t.Errorf("new.txt after fetch: %q %v", data, err)
	}
	// Fetch into the first clone after a push from the second.
	_ = os.WriteFile(filepath.Join(b, "more.txt"), []byte("more\n"), 0o644)
	_, _ = e.git(b, e.keyPath, nil, "add", "more.txt")
	_, _ = e.git(b, e.keyPath, nil, "commit", "-q", "-m", "more")
	if out, err := e.git(b, e.keyPath, nil, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push 2: %v\n%s", err, out)
	}
	if out, err := e.git(a, e.keyPath, nil, "pull", "-q", "--ff-only"); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(a, "more.txt")); err != nil {
		t.Error("more.txt missing after pull")
	}
	sessions, _ := e.metrics.snapshot()
	for _, s := range sessions {
		if !strings.HasSuffix(s, ":true") {
			t.Errorf("unsuccessful session recorded: %v", sessions)
		}
	}
	if !strings.Contains(e.logs.String(), "op=receive") || !strings.Contains(e.logs.String(), "account=alice") {
		t.Errorf("logs lack receive session:\n%s", e.logs.String())
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	other := filepath.Join(e.dir, "mallory_ed25519")
	writeClientKey(t, other)
	out, err := e.git(e.dir, other, nil, "clone", "-q", e.url("proj"), filepath.Join(e.dir, "x"))
	if err == nil {
		t.Fatalf("clone with unknown key succeeded:\n%s", out)
	}
	if !strings.Contains(out, "Permission denied") {
		t.Errorf("unexpected failure output:\n%s", out)
	}
	if sessions, _ := e.metrics.snapshot(); len(sessions) != 0 {
		t.Errorf("sessions recorded for unauthenticated client: %v", sessions)
	}
}

func TestPasswordAuthUnavailable(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	out, code, _ := e.ssh(e.keyPath, []string{"-o", "PreferredAuthentications=password,keyboard-interactive",
		"-o", "PubkeyAuthentication=no"}, "git-upload-pack 'alice/proj'")
	if code == 0 || !strings.Contains(out, "Permission denied") {
		t.Errorf("password auth: code=%d out=%s", code, out)
	}
}

func TestPushToReadOnlyRepo(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	a := filepath.Join(e.dir, "clone-ro")
	if out, err := e.git(e.dir, e.keyPath, nil, "clone", "-q", e.url("readonly"), a); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	_ = os.WriteFile(filepath.Join(a, "x"), []byte("x\n"), 0o644)
	_, _ = e.git(a, e.keyPath, nil, "add", "x")
	_, _ = e.git(a, e.keyPath, nil, "commit", "-q", "-m", "x")
	out, err := e.git(a, e.keyPath, nil, "push", "-q", "origin", "main")
	if err == nil {
		t.Fatalf("push to read-only repo succeeded:\n%s", out)
	}
	if !strings.Contains(out, "forge: forbidden") {
		t.Errorf("stderr lacks forbidden message:\n%s", out)
	}
	sessions, _ := e.metrics.snapshot()
	if len(sessions) != 2 || sessions[1] != "receive:false" {
		t.Errorf("metrics: %v", sessions)
	}
}

func TestMissingRepo(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	out, err := e.git(e.dir, e.keyPath, nil, "clone", "-q", e.url("nope"), filepath.Join(e.dir, "x"))
	if err == nil || !strings.Contains(out, "forge: repository 'alice/nope' not found") {
		t.Errorf("missing repo: err=%v\n%s", err, out)
	}
}

func TestShellAndNonGitExecRefused(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	// Plain login: the client requests a shell.
	// OpenSSH exits 255 itself when the shell request is refused. Whether
	// it flushes the stderr explanation before exiting is client timing;
	// TestGoClientRestrictions checks the message deterministically.
	out, code, _ := e.ssh(e.keyPath, []string{"-T"})
	if code == 0 || !strings.Contains(out, "shell request failed") {
		t.Errorf("shell: code=%d out=%q", code, out)
	}
	// With a pty request first (also refused).
	out, code, _ = e.ssh(e.keyPath, []string{"-tt"})
	if code == 0 || !strings.Contains(out, "PTY allocation request failed") {
		t.Errorf("shell with pty: code=%d out=%q", code, out)
	}
	if n := strings.Count(e.logs.String(), "ssh shell refused"); n != 2 {
		t.Errorf("server refused %d shells, want 2", n)
	}
	// Exec of a non-git command.
	out, code, _ = e.ssh(e.keyPath, nil, "true")
	if code != 1 || !strings.Contains(out, "only git-upload-pack and git-receive-pack") {
		t.Errorf("exec true: code=%d out=%q", code, out)
	}
	out, code, _ = e.ssh(e.keyPath, nil, "git-upload-archive 'alice/proj'")
	if code != 1 || !strings.Contains(out, "not supported") {
		t.Errorf("upload-archive: code=%d out=%q", code, out)
	}
	sessions, _ := e.metrics.snapshot()
	for _, s := range sessions {
		if s != "invalid:false" {
			t.Errorf("metrics: %v", sessions)
		}
	}
	if strings.Contains(e.logs.String(), "op=upload") {
		t.Errorf("a git session was run:\n%s", e.logs.String())
	}
}

func TestTraversalRejected(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	for _, cmd := range []string{
		`git-upload-pack "../../etc"`,
		`git-upload-pack '../../etc'`,
		`git-upload-pack ../../etc`,
		`git-upload-pack '/etc/passwd'`,
		`git-upload-pack 'alice/proj' ; id`,
	} {
		out, code, _ := e.ssh(e.keyPath, nil, cmd)
		if code != 1 || !strings.HasPrefix(out, "forge: ") {
			t.Errorf("%s: code=%d out=%q", cmd, code, out)
		}
		if strings.Contains(out, "uid=") {
			t.Fatalf("%s: command executed: %q", cmd, out)
		}
	}
}

func TestRemotePortForwardRefused(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	out, code, _ := e.ssh(e.keyPath, []string{"-N", "-o", "ExitOnForwardFailure=yes", "-R", "0:127.0.0.1:1"})
	if code != 255 {
		t.Errorf("-R: code=%d out=%q", code, out)
	}
}

func TestLocalPortForwardRefused(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	// Pick a free local port for ssh to listen on.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lport := strconv.Itoa(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	args := append(e.sshOpts(e.keyPath), "-p", e.port(), "-N", "-L", lport+":127.0.0.1:"+e.port(), "git@127.0.0.1")
	cmd := exec.Command("ssh", args...)
	cmd.Env = gitEnv(e.dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	// Wait for the forwarder to listen, then try to use it.
	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", "127.0.0.1:"+lport, time.Second)
		if err == nil {
			break
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("ssh -L never listened: %v (stderr %q)", err, stderr.String())
	}
	defer conn.Close()
	// The server must reject the direct-tcpip channel: the forwarded
	// connection closes without any data (an accepted forward to our own
	// port would yield an SSH banner).
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 64)
	n, rerr := conn.Read(buf)
	if n != 0 || rerr == nil {
		t.Errorf("forwarded connection produced data %q err=%v", buf[:n], rerr)
	}
	if !strings.Contains(e.logs.String(), "type=direct-tcpip") {
		t.Errorf("server did not log the rejected channel:\n%s", e.logs.String())
	}
}

func TestSubsystemRefused(t *testing.T) {
	requireSSH(t)
	e := newTestEnv(t)
	out, code, _ := e.ssh(e.keyPath, []string{"-s"}, "sftp")
	if code == 0 {
		t.Errorf("sftp subsystem accepted: code=%d out=%q", code, out)
	}
	if sessions, _ := e.metrics.snapshot(); len(sessions) != 0 {
		t.Errorf("sessions recorded: %v", sessions)
	}
}

// --- tests with the Go ssh client (no ssh binary needed) -----------------

func (e *testEnv) goClient(t *testing.T) *ssh.Client {
	t.Helper()
	data, err := os.ReadFile(e.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ssh.Dial("tcp", e.addr.String(), &ssh.ClientConfig{
		User:            "whatever",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestGoClientRestrictions(t *testing.T) {
	e := newTestEnv(t)
	c := e.goClient(t)
	if string(c.ServerVersion()) != serverVersion {
		t.Errorf("server version %q", c.ServerVersion())
	}
	// Non-session channels.
	if _, err := c.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Error("direct-tcpip accepted")
	}
	if _, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Error("tcpip-forward accepted")
	}
	if _, _, err := c.OpenChannel("x11", nil); err == nil {
		t.Error("x11 channel accepted")
	}
	if _, _, err := c.OpenChannel("auth-agent@openssh.com", nil); err == nil {
		t.Error("agent channel accepted")
	}
	// Session requests.
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 24, 80, nil); err == nil {
		t.Error("pty-req accepted")
	}
	if err := sess.Setenv("LD_PRELOAD", "/tmp/evil.so"); err == nil {
		t.Error("arbitrary env accepted")
	}
	if err := sess.Setenv("GIT_PROTOCOL", "version=2:foo"); err == nil {
		t.Error("GIT_PROTOCOL with extra value accepted")
	}
	if err := sess.Setenv("GIT_PROTOCOL", "version=2"); err != nil {
		t.Errorf("GIT_PROTOCOL=version=2 refused: %v", err)
	}
	if err := sess.RequestSubsystem("sftp"); err == nil {
		t.Error("subsystem accepted")
	}
	if ok, err := sess.SendRequest("auth-agent-req@openssh.com", true, nil); ok || err != nil {
		t.Errorf("agent forwarding accepted: %v %v", ok, err)
	}
	if ok, err := sess.SendRequest("x11-req", true, nil); ok || err != nil {
		t.Errorf("x11-req accepted: %v %v", ok, err)
	}
	// Shell: refused with a message on stderr and exit-status 1.
	stderr, err := sess.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err == nil {
		t.Error("shell accepted")
	}
	msg, _ := io.ReadAll(stderr)
	if !strings.Contains(string(msg), "interactive shell not available") {
		t.Errorf("shell stderr: %q", msg)
	}
}

func TestGoClientExecOnce(t *testing.T) {
	e := newTestEnv(t)
	c := e.goClient(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	stdin, _ := sess.StdinPipe()
	if err := sess.Start("git-upload-pack 'alice/proj.git'"); err != nil {
		t.Fatal(err)
	}
	// A second exec on the same session must be refused.
	if ok, err := sess.SendRequest("exec", true, ssh.Marshal(execRequest{Command: "git-upload-pack 'alice/proj.git'"})); ok || err != nil {
		t.Errorf("second exec: ok=%v err=%v", ok, err)
	}
	// Flush packet: upload-pack (v0) exits cleanly.
	_, _ = io.WriteString(stdin, "0000")
	_ = stdin.Close()
	if err := sess.Wait(); err != nil {
		t.Errorf("wait: %v stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "refs/heads/main") {
		t.Errorf("ref advertisement missing: %q", stdout.String())
	}
}

func TestSessionTimeoutKillsGit(t *testing.T) {
	e := newTestEnv(t, func(s *Server) { s.Config.SessionTimeout = 500 * time.Millisecond })
	c := e.goClient(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	stdin, _ := sess.StdinPipe()
	defer stdin.Close()
	start := time.Now()
	if err := sess.Start("git-upload-pack 'alice/proj.git'"); err != nil {
		t.Fatal(err)
	}
	// Never send anything: the session must end by timeout.
	err = sess.Wait()
	if err == nil {
		t.Error("session succeeded despite timeout")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("timeout took %v", d)
	}
	if !strings.Contains(stderr.String(), "session timed out") {
		t.Errorf("stderr: %q", stderr.String())
	}
	if !strings.Contains(e.logs.String(), "timeout=true") {
		t.Errorf("log lacks timeout:\n%s", e.logs.String())
	}
}

func TestConnectionLimits(t *testing.T) {
	e := newTestEnv(t, func(s *Server) { s.Config.MaxConnsPerIP = 2 })
	c1 := e.goClient(t)
	c2 := e.goClient(t)
	// Third connection from the same IP is closed before any handshake.
	conn, err := net.Dial("tcp", e.addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	if n, err := conn.Read(buf); err == nil {
		t.Errorf("third connection got %q", buf[:n])
	}
	_ = c1.Close()
	_ = c2.Close()
	// After release a new client is admitted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, conns := e.metrics.snapshot(); conns == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.goClient(t)
}

func TestHandshakeTimeout(t *testing.T) {
	e := newTestEnv(t, func(s *Server) { s.Config.HandshakeTimeout = 300 * time.Millisecond })
	conn, err := net.Dial("tcp", e.addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Read the banner, then stall.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), serverVersion) {
		t.Fatalf("banner: %q %v", buf[:n], err)
	}
	start := time.Now()
	_, _ = io.ReadAll(conn) // until the server closes
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stalled handshake lasted %v", d)
	}
}
