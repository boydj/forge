package sshd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// readClientKey returns the public half of an OpenSSH private key file.
func readClientKey(t *testing.T, path string) ssh.PublicKey {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

// inProcessForwarder relays a push straight into another Server's
// ServeForwardedPush: the leader half of forwarding without the control
// plane in between (internal/repl tests the transport).
type inProcessForwarder struct {
	leader *Server
	fail   error
}

func (f inProcessForwarder) ForwardReceivePack(ctx context.Context, _ string, push ForwardedPush, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if f.fail != nil {
		return 0, f.fail
	}
	return f.leader.ServeForwardedPush(ctx, push, stdin, stdout, stderr), nil
}

// forwardingReplica starts a server that leads nothing: writes to every
// known repository are answered with NotLeaderError{"b"} and relayed
// through fwd.
func forwardingReplica(t *testing.T, fwd PushForwarder) *testEnv {
	t.Helper()
	return newTestEnv(t, func(s *Server) {
		z := s.Authz.(*testAuthz)
		z.leaders = map[string]string{"alice/proj": "b", "alice/readonly": "b"}
		s.Forwarder = fwd
	})
}

func commitOnClone(t *testing.T, e *testEnv, dir, file string) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644)
	if out, err := e.git(dir, e.keyPath, nil, "add", file); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := e.git(dir, e.keyPath, nil, "commit", "-q", "-m", "add "+file); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
}

func TestPushForwardedToLeader(t *testing.T) {
	requireSSH(t)
	leader := newTestEnv(t)
	replica := forwardingReplica(t, inProcessForwarder{leader: leader.srv})
	// Alice's key differs per env; register the replica's client key on the
	// leader too so the identity the replica asserts exists there.
	leader.auth.add(readClientKey(t, replica.keyPath), &Account{ID: 7, Name: "alice"})

	work := filepath.Join(replica.dir, "clone")
	if out, err := replica.git(replica.dir, replica.keyPath, nil, "clone", "-q", replica.url("proj"), work); err != nil {
		t.Fatalf("clone via replica: %v\n%s", err, out)
	}
	commitOnClone(t, replica, work, "forwarded.txt")
	// --force: the two fixtures' initial commits differ when their
	// timestamps do, and this test is about where the push runs, not
	// fast-forwardness.
	out, err := replica.git(work, replica.keyPath, nil, "push", "--force", "origin", "main")
	if err != nil {
		t.Fatalf("push via replica: %v\n%s", err, out)
	}
	want, _ := replica.git(work, replica.keyPath, nil, "rev-parse", "HEAD")

	// The leader's repository has the commit; the replica's does not
	// (replication, not forwarding, would bring it there).
	leaderBare := filepath.Join(leader.dir, "repos", "alice", "proj.git")
	got, _ := replica.git(leaderBare, replica.keyPath, nil, "rev-parse", "refs/heads/main")
	if got != want {
		t.Errorf("leader main %q want %q", got, want)
	}
	replicaBare := filepath.Join(replica.dir, "repos", "alice", "proj.git")
	if got, _ := replica.git(replicaBare, replica.keyPath, nil, "rev-parse", "refs/heads/main"); got == want {
		t.Error("replica repository was written directly")
	}
	sessions, _ := replica.metrics.snapshot()
	if len(sessions) != 2 || sessions[1] != "receive:true" {
		t.Errorf("replica sessions: %v", sessions)
	}
	if !strings.Contains(replica.logs.String(), "push forwarded") || !strings.Contains(replica.logs.String(), "leader=b") {
		t.Errorf("replica log lacks forward record:\n%s", replica.logs.String())
	}
	if l := leader.logs.String(); !strings.Contains(l, "op=receive") || !strings.Contains(l, "account=alice") {
		t.Errorf("leader log lacks receive session:\n%s", l)
	}
	// Fetches are never forwarded: a second clone through the replica sees
	// the replica's (older) history.
	again := filepath.Join(replica.dir, "clone2")
	if out, err := replica.git(replica.dir, replica.keyPath, nil, "clone", "-q", replica.url("proj"), again); err != nil {
		t.Fatalf("second clone: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(again, "forwarded.txt")); err == nil {
		t.Error("clone through replica served the leader's history")
	}
}

func TestForwardedPushRefusedOnLeader(t *testing.T) {
	requireSSH(t)
	leader := newTestEnv(t)
	replica := forwardingReplica(t, inProcessForwarder{leader: leader.srv})
	leader.auth.add(readClientKey(t, replica.keyPath), &Account{ID: 7, Name: "alice"})
	work := filepath.Join(replica.dir, "ro")
	if out, err := replica.git(replica.dir, replica.keyPath, nil, "clone", "-q", replica.url("readonly"), work); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	commitOnClone(t, replica, work, "nope.txt")
	out, err := replica.git(work, replica.keyPath, nil, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push to readonly via replica succeeded:\n%s", out)
	}
	// The leader's refusal, not the replica's, reaches the client.
	if !strings.Contains(out, "write access to 'alice/readonly' denied") {
		t.Errorf("leader refusal not relayed:\n%s", out)
	}
	if strings.Contains(out, "node b") {
		t.Errorf("replica message leaked into a relayed refusal:\n%s", out)
	}
	if sessions, _ := replica.metrics.snapshot(); len(sessions) != 2 || sessions[1] != "receive:false" {
		t.Errorf("replica sessions: %v", sessions)
	}
}

func TestPushLeaderUnreachable(t *testing.T) {
	requireSSH(t)
	replica := forwardingReplica(t, inProcessForwarder{fail: errors.New("dial: connection refused")})
	work := filepath.Join(replica.dir, "clone")
	if out, err := replica.git(replica.dir, replica.keyPath, nil, "clone", "-q", replica.url("proj"), work); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	commitOnClone(t, replica, work, "x.txt")
	out, err := replica.git(work, replica.keyPath, nil, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push succeeded with the leader down:\n%s", out)
	}
	if !strings.Contains(out, "accepted by node b") || !strings.Contains(out, "leader unreachable") {
		t.Errorf("unexpected refusal:\n%s", out)
	}
	if !strings.Contains(replica.logs.String(), "push forward failed") {
		t.Errorf("log lacks failure record:\n%s", replica.logs.String())
	}
}

func TestPushRefusedWithoutForwarder(t *testing.T) {
	requireSSH(t)
	replica := forwardingReplica(t, nil)
	work := filepath.Join(replica.dir, "clone")
	if out, err := replica.git(replica.dir, replica.keyPath, nil, "clone", "-q", replica.url("proj"), work); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	commitOnClone(t, replica, work, "x.txt")
	out, err := replica.git(work, replica.keyPath, nil, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push succeeded on a non-leader without forwarding:\n%s", out)
	}
	if !strings.Contains(out, "accepted by node b") || strings.Contains(out, "unreachable") {
		t.Errorf("unexpected refusal:\n%s", out)
	}
}

func TestServeForwardedPushValidates(t *testing.T) {
	leader := newTestEnv(t)
	var errb strings.Builder
	cases := []ForwardedPush{
		{Owner: "alice", Repo: "../etc", AccountID: 7, Account: "alice"},
		{Owner: "Alice", Repo: "proj", AccountID: 7, Account: "alice"},
		{Owner: "alice", Repo: "proj", AccountID: 0, Account: "alice"},
		{Owner: "alice", Repo: "proj", AccountID: 7, Account: ""},
	}
	for _, c := range cases {
		errb.Reset()
		if code := leader.srv.ServeForwardedPush(context.Background(), c, strings.NewReader(""), io.Discard, &errb); code == 0 {
			t.Errorf("%+v accepted", c)
		}
		if !strings.Contains(errb.String(), "invalid request") {
			t.Errorf("%+v: stderr %q", c, errb.String())
		}
	}
	// A repository this leader does not know is refused like a local push.
	errb.Reset()
	code := leader.srv.ServeForwardedPush(context.Background(), ForwardedPush{Owner: "alice", Repo: "missing", AccountID: 7, Account: "alice"}, strings.NewReader(""), io.Discard, &errb)
	if code == 0 || !strings.Contains(errb.String(), "not found") {
		t.Errorf("missing repo: %d %q", code, errb.String())
	}
}
