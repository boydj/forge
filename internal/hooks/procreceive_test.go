package hooks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"as215520.net/forge/internal/vcs/git"
)

const (
	zero = "0000000000000000000000000000000000000000"
	oidA = "1111111111111111111111111111111111111111"
	oidB = "2222222222222222222222222222222222222222"
)

// fakeDaemon answers hook requests on a Unix socket with canned responses.
type fakeDaemon struct {
	sock string
	reqs chan Request
	l    net.Listener
}

func startFakeDaemon(t *testing.T, respond func(*Request) *Response) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "forgehook")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := &fakeDaemon{sock: filepath.Join(dir, "s"), reqs: make(chan Request, 16)}
	d.l, err = net.Listen("unix", d.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.l.Close() })
	go func() {
		for {
			conn, err := d.l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				d.reqs <- req
				_ = json.NewEncoder(conn).Encode(respond(&req))
			}()
		}
	}()
	return d
}

func (d *fakeDaemon) request(t *testing.T) Request {
	t.Helper()
	select {
	case r := <-d.reqs:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("daemon received no request")
		return Request{}
	}
}

// pkt encodes a pkt-line conversation: "" is a flush packet.
func pkt(lines ...string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		if l == "" {
			b.WriteString("0000")
			continue
		}
		fmt.Fprintf(&b, "%04x%s\n", len(l)+5, l)
	}
	return b.Bytes()
}

// unpkt decodes pkt-lines; flush packets become "".
func unpkt(t *testing.T, b []byte) []string {
	t.Helper()
	pr := &pktReader{r: bufio.NewReader(bytes.NewReader(b))}
	var out []string
	for {
		line, flush, err := pr.readLine()
		if err == io.ErrUnexpectedEOF {
			return out
		}
		if err != nil {
			t.Fatalf("unpkt: %v", err)
		}
		if flush {
			out = append(out, "")
		} else {
			out = append(out, line)
		}
		if len(out) > 100 {
			t.Fatal("unpkt: runaway")
		}
	}
}

func TestProcReceiveConversation(t *testing.T) {
	d := startFakeDaemon(t, func(req *Request) *Response {
		return &Response{OK: true, Messages: []string{"forge: created change 13 (v1, 2 commits) targeting main"},
			Results: []Result{
				{Ref: "refs/for/main", OK: true, RefName: "refs/changes/13/v1", OldOID: zero, NewOID: oidA},
				{Ref: "refs/changes/12", OK: false, Reason: "change 12 is merged\nsecond line"},
			}}
	})
	t.Setenv(EnvSocket, d.sock)
	t.Setenv(EnvAccount, "alice")
	t.Setenv(EnvAccountID, "7")
	t.Setenv(EnvRepo, "alice/proj")
	in := pkt("version=1\x00push-options atomic", "",
		zero+" "+oidA+" refs/for/main", oidA+" "+oidB+" refs/changes/12", "",
		"topic=x", "title=Hello world", "")
	var out, errb bytes.Buffer
	if err := Run("proc-receive", bytes.NewReader(in), &out, &errb); err != nil {
		t.Fatal(err)
	}
	want := []string{"version=1\x00push-options", "",
		"ok refs/for/main", "option refname refs/changes/13/v1", "option old-oid " + zero, "option new-oid " + oidA,
		"ng refs/changes/12 change 12 is merged second line", ""}
	if got := unpkt(t, out.Bytes()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("hook output:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(errb.String(), "created change 13") {
		t.Errorf("stderr: %q", errb.String())
	}
	req := d.request(t)
	if req.Hook != "proc-receive" || req.Account != "alice" || req.AccountID != 7 || req.Repo != "alice/proj" {
		t.Errorf("request identity: %+v", req)
	}
	if len(req.Updates) != 2 || req.Updates[0] != (Update{Old: zero, New: oidA, Ref: "refs/for/main"}) ||
		req.Updates[1].Ref != "refs/changes/12" {
		t.Errorf("updates: %+v", req.Updates)
	}
	if strings.Join(req.PushOptions, ",") != "topic=x,title=Hello world" {
		t.Errorf("push options: %q", req.PushOptions)
	}
}

func TestProcReceiveNoPushOptions(t *testing.T) {
	d := startFakeDaemon(t, func(req *Request) *Response {
		return &Response{OK: true, Results: []Result{{Ref: "refs/for/main", OK: true}}}
	})
	t.Setenv(EnvSocket, d.sock)
	// No push-options feature: the hook must not wait for an options block.
	in := pkt("version=1\x00atomic", "", zero+" "+oidA+" refs/for/main", "")
	var out, errb bytes.Buffer
	if err := Run("proc-receive", bytes.NewReader(in), &out, &errb); err != nil {
		t.Fatal(err)
	}
	want := []string{"version=1", "", "ok refs/for/main", ""}
	if got := unpkt(t, out.Bytes()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("hook output: %q", got)
	}
	if req := d.request(t); req.PushOptions != nil {
		t.Errorf("push options: %q", req.PushOptions)
	}
}

func TestProcReceiveMissingResultAndUnavailable(t *testing.T) {
	d := startFakeDaemon(t, func(req *Request) *Response {
		return &Response{OK: false, Messages: []string{"forge: nope"}}
	})
	t.Setenv(EnvSocket, d.sock)
	in := pkt("version=1\x00push-options", "", zero+" "+oidA+" refs/for/main", "", "")
	var out, errb bytes.Buffer
	if err := Run("proc-receive", bytes.NewReader(in), &out, &errb); err != nil {
		t.Fatal(err)
	}
	if got := unpkt(t, out.Bytes()); got[2] != "ng refs/for/main no result from forge" {
		t.Errorf("missing result: %q", got)
	}
	d.request(t)

	t.Setenv(EnvSocket, filepath.Join(t.TempDir(), "missing.sock"))
	in = pkt("version=1\x00push-options", "", zero+" "+oidA+" refs/for/main", oidA+" "+oidB+" refs/changes/12", "", "")
	out.Reset()
	errb.Reset()
	if err := Run("proc-receive", bytes.NewReader(in), &out, &errb); err != nil {
		t.Fatal(err)
	}
	want := []string{"version=1\x00push-options", "", "ng refs/for/main forge unavailable", "ng refs/changes/12 forge unavailable", ""}
	if got := unpkt(t, out.Bytes()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unavailable: %q", got)
	}
	if !strings.Contains(errb.String(), "hook error") {
		t.Errorf("stderr: %q", errb.String())
	}
}

func TestProcReceiveProtocolErrors(t *testing.T) {
	t.Setenv(EnvSocket, filepath.Join(t.TempDir(), "missing.sock"))
	for name, in := range map[string][]byte{
		"bad version": pkt("version=2\x00push-options", "", zero+" "+oidA+" refs/for/main", ""),
		"no version":  pkt("", zero+" "+oidA+" refs/for/main", ""),
		"truncated":   pkt("version=1", "")[:8],
		"bad length":  []byte("zzzz"),
		"short":       []byte("0002"),
		"oversize":    []byte("fff1"),
		"bad command": pkt("version=1", "", "just two", ""),
		"no commands": pkt("version=1", "", ""),
	} {
		var out, errb bytes.Buffer
		if err := Run("proc-receive", bytes.NewReader(in), &out, &errb); err == nil {
			t.Errorf("%s: no error (output %q)", name, out.String())
		}
	}
}

func TestPktLineWrite(t *testing.T) {
	var b bytes.Buffer
	_ = writePkt(&b, "ok refs/for/main")
	_ = writeFlush(&b)
	if b.String() != "0015ok refs/for/main\n0000" {
		t.Errorf("%q", b.String())
	}
	b.Reset()
	_ = writePkt(&b, strings.Repeat("x", 70000))
	if b.Len() != pktMaxLen {
		t.Errorf("oversize pkt not clamped: %d", b.Len())
	}
	if got := unpkt(t, b.Bytes()); len(got) != 1 || len(got[0]) != pktMaxPayload-1 {
		t.Errorf("clamped pkt: %d", len(got[0]))
	}
}

// TestHelperProcess is the proc-receive hook body for the end-to-end test:
// the hook script execs the test binary with FORGE_TEST_HOOK_HELPER=1.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("FORGE_TEST_HOOK_HELPER") != "1" {
		return
	}
	if err := Run("proc-receive", os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hook:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestProcReceiveEndToEnd pushes through a real git-receive-pack configured
// with Backend.Env() and answers open question 1 of ADR 0012: a command
// for refs/changes/12 reaches the hook even though refs/changes/12/head
// exists (the D/F check lives in the ref transaction, which proc-receive
// commands bypass), and refs/for/<branch> is routed as well.
func TestProcReceiveEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hooksDir := t.TempDir()
	script := "#!/bin/sh\nexec \"" + exe + "\" -test.run='^TestHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "proc-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := git.New(git.Options{HomeDir: t.TempDir(), HooksDir: hooksDir})
	if err != nil {
		t.Skip("git backend:", err)
	}
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "proj.git")
	if err := b.Init(ctx, bare, "main"); err != nil {
		t.Fatal(err)
	}

	d := startFakeDaemon(t, func(req *Request) *Response {
		if len(req.Updates) != 1 {
			return &Response{OK: false, Messages: []string{"forge: expected one command"}}
		}
		u := req.Updates[0]
		switch u.Ref {
		case "refs/changes/12":
			return &Response{OK: true, Messages: []string{"forge: updated change 12 (v2)"},
				Results: []Result{{Ref: u.Ref, OK: true, RefName: "refs/changes/12/v2", OldOID: zero, NewOID: u.New}}}
		case "refs/for/main":
			return &Response{OK: true, Messages: []string{"forge: created change 13 (v1)"},
				Results: []Result{{Ref: u.Ref, OK: true, RefName: "refs/changes/13/v1", OldOID: zero, NewOID: u.New}}}
		}
		return &Response{OK: false, Results: []Result{{Ref: u.Ref, Reason: "unexpected ref"}}}
	})

	// In production the SSH server spawns receive-pack with Backend.Env().
	// git clears GIT_CONFIG_COUNT (local_repo_env) when the file transport
	// spawns receive-pack itself, so model the server with a --receive-pack
	// wrapper that exports the environment and execs git receive-pack.
	serverEnv := b.Env(
		EnvSocket+"="+d.sock, EnvAccount+"=alice", EnvAccountID+"=7", EnvRepo+"=alice/proj",
		"FORGE_TEST_HOOK_HELPER=1")
	var rp strings.Builder
	rp.WriteString("#!/bin/sh\n")
	for _, kv := range serverEnv {
		fmt.Fprintf(&rp, "export '%s'\n", strings.ReplaceAll(kv, "'", "'\\''"))
	}
	rp.WriteString("exec git receive-pack \"$@\"\n")
	rpScript := filepath.Join(t.TempDir(), "receive-pack")
	if err := os.WriteFile(rpScript, []byte(rp.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.org",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.org")
	work := filepath.Join(t.TempDir(), "work")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	push := func(args ...string) string {
		t.Helper()
		return run(work, append([]string{"push", "--receive-pack=" + rpScript}, args...)...)
	}
	_ = os.MkdirAll(work, 0o755)
	run(work, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644)
	run(work, "add", "a.txt")
	run(work, "commit", "-q", "-m", "one")
	run(work, "push", "-q", bare, "main")
	// An existing change: refs/changes/12/head makes refs/changes/12 a
	// directory in the ref namespace.
	run(bare, "update-ref", "refs/changes/12/head", "refs/heads/main")
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\nb\n"), 0o644)
	run(work, "commit", "-q", "-am", "two")
	tip := strings.TrimSpace(run(work, "rev-parse", "HEAD"))

	out := push("-o", "topic=x", "-o", "title=Hello world", bare, "HEAD:refs/changes/12")
	t.Logf("push HEAD:refs/changes/12 output:\n%s", out)
	req := d.request(t)
	if len(req.Updates) != 1 || req.Updates[0].Ref != "refs/changes/12" || req.Updates[0].New != tip || req.Updates[0].Old != zero {
		t.Errorf("hook saw commands %+v", req.Updates)
	}
	if strings.Join(req.PushOptions, ",") != "topic=x,title=Hello world" {
		t.Errorf("push options: %q", req.PushOptions)
	}
	if req.Account != "alice" || req.AccountID != 7 || req.Repo != "alice/proj" || req.Hook != "proc-receive" {
		t.Errorf("identity: %+v", req)
	}
	if !strings.Contains(out, "refs/changes/12/v2") || !strings.Contains(out, "updated change 12") {
		t.Errorf("push output:\n%s", out)
	}
	// receive-pack does not touch refs for proc-receive commands: the
	// daemon owns the update. Neither refs/changes/12 nor .../v2 exists.
	if o, _ := exec.Command("git", "-C", bare, "for-each-ref", "refs/changes/").Output(); !strings.HasSuffix(strings.TrimSpace(string(o)), "refs/changes/12/head") || strings.Count(string(o), "\n") != 1 {
		t.Errorf("refs after push:\n%s", o)
	}

	// refs/for/<branch> is routed by the second procReceiveRefs entry.
	out = push(bare, "HEAD:refs/for/main")
	req = d.request(t)
	if len(req.Updates) != 1 || req.Updates[0].Ref != "refs/for/main" || req.PushOptions != nil {
		t.Errorf("refs/for command: %+v", req)
	}
	if !strings.Contains(out, "refs/changes/13/v1") {
		t.Errorf("push output:\n%s", out)
	}

	// A rejection surfaces per ref and fails the push.
	cmd := exec.Command("git", "push", "--receive-pack="+rpScript, bare, "HEAD:refs/for/other")
	cmd.Dir = work
	cmd.Env = env
	o, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(o), "unexpected ref") {
		t.Errorf("rejected push: err=%v\n%s", err, o)
	}
	d.request(t)

	// Ordinary branch pushes are untouched (no hook, no daemon call).
	push("-q", bare, "HEAD:refs/heads/side")
	select {
	case r := <-d.reqs:
		t.Errorf("branch push reached proc-receive: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}
