package repl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

const testSecret = "test-secret-0123456789abcdef"

// testNode is one in-process forge node: its own data directory, store,
// git backend and control server on 127.0.0.1.
type testNode struct {
	name string
	dir  string
	st   *store.Store
	git  *gitvcs.Backend
	node *Node
	addr string
}

func (tn *testNode) repoPath(owner, name string) string {
	return filepath.Join(tn.dir, "repos", owner, name+".git")
}

// newCluster builds two nodes "a" and "b" peered with each other. "a" is
// the metadata leader (first in sorted order).
func newCluster(t *testing.T) (*testNode, *testNode) {
	t.Helper()
	ctx := context.Background()
	mk := func(name string) (*testNode, net.Listener) {
		dir := t.TempDir()
		st, err := store.Open(ctx, filepath.Join(dir, "forge.db"), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		g, err := gitvcs.New(gitvcs.Options{HomeDir: dir, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		return &testNode{name: name, dir: dir, st: st, git: g, addr: l.Addr().String()}, l
	}
	a, la := mk("a")
	b, lb := mk("b")
	start := func(tn *testNode, l net.Listener, peer *testNode, reg *metrics.Registry) {
		n, err := New(Options{
			Name: tn.name, Peers: map[string]string{peer.name: peer.addr}, Secret: testSecret,
			ReposDir: filepath.Join(tn.dir, "repos"), AssetsDir: filepath.Join(tn.dir, "assets"), Version: "test", Store: tn.st, Git: tn.git, Metrics: reg,
		})
		if err != nil {
			t.Fatal(err)
		}
		tn.node = n
		srv := &http.Server{Handler: n.Handler()}
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(func() { _ = srv.Close() })
	}
	start(a, la, b, nil)
	start(b, lb, a, metrics.New())
	if a.node.MetadataLeader() != "a" || b.node.MetadataLeader() != "a" {
		t.Fatalf("metadata leader: a=%s b=%s", a.node.MetadataLeader(), b.node.MetadataLeader())
	}
	return a, b
}

// gitCmd runs the system git in dir with a clean environment.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.org",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.org",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// pushCommit clones bare into a scratch directory, adds a commit touching
// file and pushes main (and a tag when tag is non-empty).
func pushCommit(t *testing.T, bare, file, tag string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	if out := gitCmd(t, filepath.Dir(work), "clone", "-q", bare, work); strings.Contains(out, "fatal") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		t.Fatal(err)
	}
	empty := gitCmd(t, work, "rev-list", "--all", "--count")
	if strings.TrimSpace(empty) == "0" {
		gitCmd(t, work, "checkout", "-q", "-b", "main")
	}
	if err := os.WriteFile(filepath.Join(work, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", file)
	gitCmd(t, work, "commit", "-q", "-m", "add "+file)
	gitCmd(t, work, "push", "-q", bare, "HEAD:refs/heads/main")
	if tag != "" {
		gitCmd(t, work, "tag", "-a", tag, "-m", tag)
		gitCmd(t, work, "push", "-q", bare, "refs/tags/"+tag)
	}
}

// recordPush does what the post-receive hook does on a leader: stamp the
// repository and append a push event.
func recordPush(t *testing.T, tn *testNode, rp *store.Repo, size int64) {
	t.Helper()
	ctx := context.Background()
	if err := tn.st.RecordPush(ctx, rp.ID, size); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.st.AddEvent(ctx, &store.Event{Kind: store.EventPush, RepoID: rp.ID, UserID: rp.OwnerID, Subject: "pushed", Path: "/" + rp.Owner + "/" + rp.Name, Payload: json.RawMessage(`{"ref":"main"}`)}); err != nil {
		t.Fatal(err)
	}
}

func refsOf(t *testing.T, g *gitvcs.Backend, path string) []vcs.Ref {
	t.Helper()
	repo, err := g.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	refs, err := repo.Refs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func sameRefs(a, b []vcs.Ref) bool {
	w := make([]wireRef, 0, len(b))
	for _, r := range b {
		w = append(w, wireRef{Name: r.Name, Kind: int(r.Kind), Object: string(r.Object)})
	}
	return refsEqual(a, w)
}

// seed creates alice and alice/proj led by a with two commits, two events,
// an issue and a comment.
func seed(t *testing.T, a *testNode) (*store.User, *store.Repo) {
	t.Helper()
	ctx := context.Background()
	alice, err := a.st.CreateUser(ctx, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddSSHKey(ctx, &store.SSHKey{UserID: alice.ID, Fingerprint: "SHA256:abc", KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA"}); err != nil {
		t.Fatal(err)
	}
	rp, err := a.st.CreateRepo(ctx, &store.Repo{OwnerID: alice.ID, Name: "proj", DefaultBranch: "main", VCS: "git", LeaderNode: "a", Description: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.git.Init(ctx, a.repoPath("alice", "proj"), "main"); err != nil {
		t.Fatal(err)
	}
	pushCommit(t, a.repoPath("alice", "proj"), "one.txt", "v0.1")
	pushCommit(t, a.repoPath("alice", "proj"), "two.txt", "")
	if err := a.st.RecordPush(ctx, rp.ID, 1234); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddEvent(ctx, &store.Event{Kind: store.EventRepoCreate, RepoID: rp.ID, UserID: alice.ID, Subject: "created proj", Path: "/alice/proj"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddEvent(ctx, &store.Event{Kind: store.EventPush, RepoID: rp.ID, UserID: alice.ID, Subject: "pushed", Path: "/alice/proj", Payload: json.RawMessage(`{"ref":"main"}`)}); err != nil {
		t.Fatal(err)
	}
	is, err := a.st.CreateIssue(ctx, rp.ID, alice.ID, "first issue", "body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddComment(ctx, rp.ID, "issue", is.ID, alice.ID, "a comment"); err != nil {
		t.Fatal(err)
	}
	rp, err = a.st.RepoByID(ctx, rp.ID)
	if err != nil {
		t.Fatal(err)
	}
	return alice, rp
}

func TestReplicateRepoEventsAndMetadata(t *testing.T) {
	a, b := newCluster(t)
	ctx := context.Background()
	alice, rp := seed(t, a)

	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Global metadata from the metadata leader.
	u, err := b.st.UserByName(ctx, "alice")
	if err != nil || u.ID != alice.ID {
		t.Fatalf("alice on b: %v %v", u, err)
	}
	if k, err := b.st.SSHKeyByFingerprint(ctx, "SHA256:abc"); err != nil || k.UserID != alice.ID {
		t.Fatalf("ssh key on b: %v %v", k, err)
	}
	// Repository record.
	br, err := b.st.RepoByID(ctx, rp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if br.LeaderNode != "a" || br.Owner != "alice" || br.Name != "proj" || br.Description != "test" || !br.UpdatedAt.Equal(rp.UpdatedAt) {
		t.Errorf("repo on b: %+v", br)
	}
	// Git refs identical.
	ra, rb := refsOf(t, a.git, a.repoPath("alice", "proj")), refsOf(t, b.git, b.repoPath("alice", "proj"))
	if len(ra) != 2 || !sameRefs(ra, rb) {
		t.Errorf("refs differ: a=%v b=%v", ra, rb)
	}
	// Events replicated with origin ids; cursor advanced.
	last, _ := a.st.LastLocalEventID(ctx)
	cur, _ := b.st.ReplCursor(ctx, "a")
	if cur != last || last == 0 {
		t.Errorf("cursor %d, leader last %d", cur, last)
	}
	evs, err := b.st.Events(ctx, store.EventQuery{RepoID: rp.ID, AfterID: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events on b: %d", len(evs))
	}
	for _, e := range evs {
		if e.Node != "a" || e.Owner != "alice" || e.UserName != "alice" {
			t.Errorf("event %+v", e)
		}
	}
	var originIDs int
	if err := b.st.DB().QueryRow(`SELECT count(*) FROM events WHERE node = 'a' AND origin_id IS NOT NULL`).Scan(&originIDs); err != nil || originIDs != 2 {
		t.Errorf("origin ids: %d %v", originIDs, err)
	}
	// Issue and comment rows with the leader's ids.
	is, err := b.st.IssueByNumber(ctx, rp.ID, 1)
	if err != nil || is.Title != "first issue" || is.Author != "alice" || is.Comments != 1 {
		t.Fatalf("issue on b: %+v %v", is, err)
	}
	// Replica bookkeeping.
	p, err := b.st.ReplicaFor(ctx, rp.ID, "b")
	if err != nil || p.Status != store.ReplicaOK || p.LastSyncedAt.IsZero() || p.LastEventID != last {
		t.Fatalf("replica row: %+v %v", p, err)
	}
	st, err := Status(ctx, b.st)
	if err != nil || len(st) != 1 || st[0].Repo != "alice/proj" || st[0].LeaderCursor != last {
		t.Errorf("status: %+v %v", st, err)
	}

	// A second sync is a no-op: same rows, same cursor.
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.st.ReplicatedEventCount(ctx, "a"); n != 2 {
		t.Errorf("events duplicated: %d", n)
	}

	// A new push and a new comment; an interrupted fetch leaves refs alone,
	// the retry converges.
	pushCommit(t, a.repoPath("alice", "proj"), "three.txt", "v0.2")
	recordPush(t, a, rp, 2345)
	if _, err := a.st.AddComment(ctx, rp.ID, "issue", is.ID, alice.ID, "second comment"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddEvent(ctx, &store.Event{Kind: store.EventComment, RepoID: rp.ID, UserID: alice.ID, Subject: "commented", Path: "/alice/proj/issues/1"}); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	if err := b.node.fetchRepo(short, rp, a.addr); err == nil {
		t.Fatal("fetch with expired context succeeded")
	}
	if sameRefs(refsOf(t, a.git, a.repoPath("alice", "proj")), refsOf(t, b.git, b.repoPath("alice", "proj"))) {
		t.Fatal("refs already equal before retry")
	}
	// A failing sync is recorded on the replica row...
	if err := os.Rename(a.repoPath("alice", "proj"), a.repoPath("alice", "proj")+".away"); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err == nil {
		t.Fatal("sync succeeded with the leader's repository missing")
	}
	if p, _ := b.st.ReplicaFor(ctx, rp.ID, "b"); p == nil || p.Status != store.ReplicaError || !strings.Contains(p.Detail, "fetch") {
		t.Fatalf("replica row after failure: %+v", p)
	}
	// ...and the retry converges.
	if err := os.Rename(a.repoPath("alice", "proj")+".away", a.repoPath("alice", "proj")); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !sameRefs(refsOf(t, a.git, a.repoPath("alice", "proj")), refsOf(t, b.git, b.repoPath("alice", "proj"))) {
		t.Error("refs differ after retry")
	}
	if p, _ := b.st.ReplicaFor(ctx, rp.ID, "b"); p == nil || p.Status != store.ReplicaOK {
		t.Fatalf("replica row after retry: %+v", p)
	}
	if cs, err := b.st.ListComments(ctx, "issue", is.ID); err != nil || len(cs) != 2 {
		t.Errorf("comments on b: %d %v", len(cs), err)
	}
	if n, _ := b.st.ReplicatedEventCount(ctx, "a"); n != 4 {
		t.Errorf("events after retry: %d", n)
	}
}

func TestMoveLeader(t *testing.T) {
	a, b := newCluster(t)
	ctx := context.Background()
	_, rp := seed(t, a)

	// Not synced yet: refuse.
	if err := a.node.MoveLeader(ctx, rp.ID, "b"); !errors.Is(err, ErrNotSynced) && !errors.Is(err, ErrRemote) {
		t.Fatalf("move before sync: %v", err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Replica behind by one push: refuse.
	pushCommit(t, a.repoPath("alice", "proj"), "four.txt", "")
	if err := a.node.MoveLeader(ctx, rp.ID, "b"); !errors.Is(err, ErrNotSynced) {
		t.Fatalf("move with stale refs: %v", err)
	}
	recordPush(t, a, rp, 999)
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Must run on the leader.
	if err := b.node.MoveLeader(ctx, rp.ID, "a"); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("move on replica: %v", err)
	}
	if err := a.node.MoveLeader(ctx, rp.ID, "b"); err != nil {
		t.Fatalf("move: %v", err)
	}
	ar, _ := a.st.RepoByID(ctx, rp.ID)
	if ar.LeaderNode != "b" {
		t.Fatalf("leader on a: %s", ar.LeaderNode)
	}
	// b learns of the handoff from a (its recorded leader), then a replicates from b.
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	br, _ := b.st.RepoByID(ctx, rp.ID)
	if br.LeaderNode != "b" {
		t.Fatalf("leader on b: %s", br.LeaderNode)
	}
	pushCommit(t, b.repoPath("alice", "proj"), "five.txt", "v0.3")
	recordPush(t, b, rp, 5555)
	if err := a.node.SyncOnce(ctx); err != nil {
		t.Fatalf("a sync from b: %v", err)
	}
	if !sameRefs(refsOf(t, b.git, b.repoPath("alice", "proj")), refsOf(t, a.git, a.repoPath("alice", "proj"))) {
		t.Error("a did not replicate from the new leader")
	}
	if p, _ := a.st.ReplicaFor(ctx, rp.ID, "a"); p == nil || p.Status != store.ReplicaOK {
		t.Errorf("replica row on a: %+v", p)
	}
	// The leadership event replicated to b.
	evs, _ := b.st.Events(ctx, store.EventQuery{RepoID: rp.ID, Kinds: []string{store.EventAdminAction}})
	if len(evs) != 1 || !strings.Contains(evs[0].Subject, "moved to b") {
		t.Errorf("admin event on b: %+v", evs)
	}
	// Resync on the replica works, on the leader it is refused.
	if err := a.node.Resync(ctx, rp.ID); err != nil {
		t.Errorf("resync: %v", err)
	}
	if err := b.node.Resync(ctx, rp.ID); !errors.Is(err, ErrNotLeader) {
		t.Errorf("resync on leader: %v", err)
	}
}

func TestAuth(t *testing.T) {
	a, _ := newCluster(t)
	srv := httptest.NewServer(a.node.Handler())
	defer srv.Close()
	get := func(secret, node string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/status", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		req.Header.Set(headerNode, node)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := get("", "b"); s != http.StatusUnauthorized {
		t.Errorf("no secret: %d", s)
	}
	if s := get("wrong-secret-0123456789", "b"); s != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", s)
	}
	if s := get(testSecret, "mallory"); s != http.StatusForbidden {
		t.Errorf("unknown peer: %d", s)
	}
	if s := get(testSecret, "b"); s != http.StatusOK {
		t.Errorf("good: %d", s)
	}
	// Git endpoints are covered by the same gate, and pushes are refused.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/git/alice/proj.git/git-receive-pack", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	req.Header.Set(headerNode, "b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("receive-pack: %d", resp.StatusCode)
	}
	if _, err := New(Options{Name: "a", Secret: "short", ReposDir: a.dir, Store: a.st, Git: a.git}); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := New(Options{Name: "a", Secret: testSecret, ReposDir: a.dir, Store: a.st, Git: a.git, Peers: map[string]string{"a": "x"}}); err == nil {
		t.Error("self as peer accepted")
	}
}

func TestForwardAndNotify(t *testing.T) {
	a, b := newCluster(t)
	ctx := context.Background()
	var got ForwardRequest
	a.node.SetForwardHandler(HandlerFunc(func(_ context.Context, certDER []byte, path, mime string, body []byte) (int, string) {
		got = ForwardRequest{CertDER: certDER, Path: path, Mime: mime, Body: body}
		return 30, "gemini://example/alice/proj/issues/1"
	}))
	status, meta, err := b.node.Forward(ctx, "a", "/alice/proj/issues/new", "text/plain", []byte("hello"), []byte{0x30, 0x01})
	if err != nil || status != 30 || !strings.HasSuffix(meta, "/issues/1") {
		t.Fatalf("forward: %d %q %v", status, meta, err)
	}
	if got.Path != "/alice/proj/issues/new" || got.Mime != "text/plain" || string(got.Body) != "hello" || len(got.CertDER) != 2 {
		t.Errorf("leader saw %+v", got)
	}
	// Without a handler the leader answers 501.
	if _, _, err := a.node.Forward(ctx, "b", "/x", "text/plain", nil, []byte{1}); err == nil || !errors.Is(err, ErrRemote) {
		t.Errorf("forward to node without handler: %v", err)
	}
	// Notify wakes the worker.
	a.node.NotifyPeers(ctx, 42)
	select {
	case id := <-b.node.wake:
		if id != 42 {
			t.Errorf("woke with %d", id)
		}
	case <-time.After(5 * time.Second):
		t.Error("no wake")
	}
	// OnPush is the same path.
	a.node.OnPush(&store.Repo{ID: 7})
	select {
	case id := <-b.node.wake:
		if id != 7 {
			t.Errorf("woke with %d", id)
		}
	case <-time.After(5 * time.Second):
		t.Error("no wake from OnPush")
	}
}

func TestRunLoop(t *testing.T) {
	a, b := newCluster(t)
	_, rp := seed(t, a)
	b.node.opts.SyncInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.node.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if p, err := b.st.ReplicaFor(context.Background(), rp.ID, "b"); err == nil && p.Status == store.ReplicaOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never synced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}

// addAsset does what forge.AddAsset does on a leader: write the file at the
// asset path, record the row and append an event.
func addAsset(t *testing.T, tn *testNode, rp *store.Repo, rel *store.Release, name string, data []byte) *store.ReleaseAsset {
	t.Helper()
	ctx := context.Background()
	path := tn.node.AssetPath(rp, rel.Tag, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	a, err := tn.st.AddReleaseAsset(ctx, &store.ReleaseAsset{ReleaseID: rel.ID, Name: name, Size: int64(len(data)), MIME: "application/octet-stream", SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tn.st.AddEvent(ctx, &store.Event{Kind: store.EventReleaseAsset, RepoID: rp.ID, UserID: rp.OwnerID, Subject: "attached " + name, Path: "/" + rp.Owner + "/" + rp.Name}); err != nil {
		t.Fatal(err)
	}
	return a
}

// touchRepo appends an event so that replicas re-pull the metadata snapshot
// (the store's asset deletes do not stamp the repository themselves).
func touchRepo(t *testing.T, tn *testNode, rp *store.Repo) {
	t.Helper()
	if _, err := tn.st.AddEvent(context.Background(), &store.Event{Kind: store.EventAdminAction, RepoID: rp.ID, UserID: rp.OwnerID, Subject: "touch", Path: "/" + rp.Owner + "/" + rp.Name}); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) (string, int64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b))
}

func TestReplicateAssets(t *testing.T) {
	a, b := newCluster(t)
	ctx := context.Background()
	alice, rp := seed(t, a)
	rel, err := a.st.CreateRelease(ctx, rp.ID, alice.ID, "v0.1", "first", "notes")
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 300*1024+17)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	tarball := addAsset(t, a, rp, rel, "proj-0.1.tar.gz", blob)
	sig := addAsset(t, a, rp, rel, "proj-0.1.tar.gz.asc", []byte("-----BEGIN PGP SIGNATURE-----\n"))

	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	bpath := b.node.AssetPath(rp, "v0.1", "proj-0.1.tar.gz")
	if got, err := os.ReadFile(bpath); err != nil || !bytes.Equal(got, blob) {
		t.Fatalf("tarball on b: %d bytes, %v", len(got), err)
	}
	if sum, size := fileSHA256(t, bpath); sum != tarball.SHA256 || size != tarball.Size {
		t.Fatalf("tarball on b: sha256 %s size %d, want %s %d", sum, size, tarball.SHA256, tarball.Size)
	}
	if fi, err := os.Stat(bpath); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("tarball mode: %v %v", fi.Mode(), err)
	}
	if sum, _ := fileSHA256(t, b.node.AssetPath(rp, "v0.1", "proj-0.1.tar.gz.asc")); sum != sig.SHA256 {
		t.Errorf("signature on b: sha256 %s, want %s", sum, sig.SHA256)
	}
	if p, _ := b.st.ReplicaFor(ctx, rp.ID, "b"); p == nil || p.Status != store.ReplicaOK {
		t.Fatalf("replica row: %+v", p)
	}
	// The rows replicated too, and the temp dir is left clean.
	if brel, err := b.st.ReleaseByTag(ctx, rp.ID, "v0.1"); err != nil || len(brel.Assets) != 2 {
		t.Fatalf("release on b: %+v %v", brel, err)
	}
	if ents, _ := os.ReadDir(b.node.assetTmpDir()); len(ents) != 0 {
		t.Errorf("temp files left: %d", len(ents))
	}

	// A truncated copy is noticed by the cheap per-cycle check and repaired
	// without any change on the leader.
	if err := os.WriteFile(bpath, blob[:100], 0o640); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync after truncation: %v", err)
	}
	if sum, _ := fileSHA256(t, bpath); sum != tarball.SHA256 {
		t.Errorf("truncated tarball not repaired")
	}
	// Same-length corruption is only caught by the full check: a resync.
	bad := append([]byte(nil), blob...)
	bad[10] ^= 0xff
	if err := os.WriteFile(bpath, bad, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := b.node.Resync(ctx, rp.ID); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if sum, _ := fileSHA256(t, bpath); sum != tarball.SHA256 {
		t.Errorf("corrupt tarball not repaired by resync")
	}
	// A missing file is fetched again.
	if err := os.Remove(bpath); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync after removal: %v", err)
	}
	if sum, _ := fileSHA256(t, bpath); sum != tarball.SHA256 {
		t.Errorf("missing tarball not fetched")
	}

	// A file the leader cannot serve: recorded on the replica row, other
	// files unaffected, retried until the leader is fixed.
	apath := a.node.AssetPath(rp, "v0.1", "proj-0.1.tar.gz")
	if err := os.Rename(apath, apath+".away"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bpath); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err == nil {
		t.Fatal("sync succeeded with the leader's asset missing")
	}
	if p, _ := b.st.ReplicaFor(ctx, rp.ID, "b"); p == nil || p.Status != store.ReplicaError || !strings.Contains(p.Detail, "assets: v0.1/proj-0.1.tar.gz") || !strings.Contains(p.Detail, "404") {
		t.Fatalf("replica row after failure: %+v", p)
	}
	if _, err := os.Stat(b.node.AssetPath(rp, "v0.1", "proj-0.1.tar.gz.asc")); err != nil {
		t.Errorf("signature removed while tarball failed: %v", err)
	}
	if err := os.Rename(apath+".away", apath); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if p, _ := b.st.ReplicaFor(ctx, rp.ID, "b"); p == nil || p.Status != store.ReplicaOK {
		t.Fatalf("replica row after retry: %+v", p)
	}
	if sum, _ := fileSHA256(t, bpath); sum != tarball.SHA256 {
		t.Errorf("tarball not fetched on retry")
	}

	// An oversized record is refused before any transfer.
	if _, err := a.st.AddReleaseAsset(ctx, &store.ReleaseAsset{ReleaseID: rel.ID, Name: "huge.bin", Size: 2 << 30, MIME: "application/octet-stream", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	touchRepo(t, a, rp)
	if err := b.node.SyncOnce(ctx); err == nil || !strings.Contains(err.Error(), "huge.bin") {
		t.Fatalf("oversized asset: %v", err)
	}
	huge, _ := a.st.ReleaseAsset(ctx, rel.ID, "huge.bin")
	if err := a.st.DeleteReleaseAsset(ctx, huge.ID); err != nil {
		t.Fatal(err)
	}

	// Deleting a row on the leader removes the file on the replica; a stray
	// file in the tag directory goes with it, a stray tag directory's files
	// too, and an unrelated repository's files are untouched.
	if err := a.st.DeleteReleaseAsset(ctx, sig.ID); err != nil {
		t.Fatal(err)
	}
	touchRepo(t, a, rp)
	stray := b.node.AssetPath(rp, "v0.1", "stray.bin")
	if err := os.WriteFile(stray, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	strayTag := b.node.AssetPath(rp, "v9.9", "old.bin")
	if err := os.MkdirAll(filepath.Dir(strayTag), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strayTag, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(b.dir, "assets", "alice", "other", "v1", "keep.bin")
	if err := os.MkdirAll(filepath.Dir(other), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync after delete: %v", err)
	}
	for _, p := range []string{b.node.AssetPath(rp, "v0.1", "proj-0.1.tar.gz.asc"), stray, strayTag, filepath.Dir(strayTag)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still present", p)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated file removed: %v", err)
	}
	if sum, _ := fileSHA256(t, bpath); sum != tarball.SHA256 {
		t.Errorf("tarball lost during prune")
	}
	// Deleting the release removes the whole tag directory on the replica.
	if err := a.st.DeleteRelease(ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	touchRepo(t, a, rp)
	if err := b.node.SyncOnce(ctx); err != nil {
		t.Fatalf("sync after release delete: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(bpath)); err == nil {
		t.Errorf("tag directory still present on b")
	}
}

func TestAssetEndpoint(t *testing.T) {
	a, _ := newCluster(t)
	ctx := context.Background()
	alice, rp := seed(t, a)
	rel, err := a.st.CreateRelease(ctx, rp.ID, alice.ID, "v0.1", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	asset := addAsset(t, a, rp, rel, "proj.tar.gz", []byte("tarball"))
	// A secret file outside the assets tree that traversal would reach.
	if err := os.WriteFile(filepath.Join(a.dir, "secret"), []byte("s3cret"), 0o640); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.node.Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(rawPath string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+rawPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testSecret)
		req.Header.Set(headerNode, "b")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	base := "/v1/repos/" + strconv.FormatInt(rp.ID, 10) + "/assets/"
	resp, body := get(base + "v0.1/proj.tar.gz")
	if resp.StatusCode != http.StatusOK || string(body) != "tarball" || resp.Header.Get(headerSHA256) != asset.SHA256 || resp.ContentLength != 7 {
		t.Fatalf("good: %d %q sha=%q len=%d", resp.StatusCode, body, resp.Header.Get(headerSHA256), resp.ContentLength)
	}
	// Unknown tag, name, repository; unauthenticated.
	for _, p := range []string{base + "v0.1/nope.tar.gz", base + "v0.2/proj.tar.gz", "/v1/repos/999/assets/v0.1/proj.tar.gz"} {
		if resp, _ := get(p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+base+"v0.1/proj.tar.gz", nil)
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated: %v %v", resp, err)
	}
	// A file present on disk without a row is not served.
	if err := os.WriteFile(a.node.AssetPath(rp, "v0.1", "norow.bin"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(base + "v0.1/norow.bin"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("file without row: %d", resp.StatusCode)
	}
	// Traversal, plain and encoded, in either segment: never 200, never
	// the secret.
	for _, p := range []string{
		base + "../../../secret", base + "v0.1/../../../../secret", base + "..%2F..%2F..%2Fsecret",
		base + "v0.1/..%2F..%2F..%2F..%2Fsecret", base + "%2e%2e/%2e%2e/%2e%2e/secret", base + "%2e%2e%2f%2e%2e%2f%2e%2e%2fsecret",
		base + "v0.1%2F..%2F..%2F..%2Fsecret", base + "..%2Fv0.1/proj.tar.gz", base + "v0.1/%2e%2e%2Fproj.tar.gz",
		base + "v0.1/proj.tar.gz%00", base + "v0.1/.hidden", base + "v0.1/.",
	} {
		resp, body := get(p)
		if resp.StatusCode == http.StatusOK || bytes.Contains(body, []byte("s3cret")) {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
	// A replica answers 409 for repositories it does not lead.
	if err := a.st.SetRepoLeader(ctx, rp.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(base + "v0.1/proj.tar.gz"); resp.StatusCode != http.StatusConflict {
		t.Errorf("not leader: %d", resp.StatusCode)
	}
}

func TestAssetsDirDefault(t *testing.T) {
	a, _ := newCluster(t)
	n, err := New(Options{Name: "x", Secret: testSecret, ReposDir: "/srv/forge/data/repos", Store: a.st, Git: a.git})
	if err != nil {
		t.Fatal(err)
	}
	if got := n.AssetPath(&store.Repo{Owner: "alice", Name: "proj"}, "v1", "a.tgz"); got != "/srv/forge/data/assets/alice/proj/v1/a.tgz" {
		t.Errorf("default asset path: %s", got)
	}
	if !validAsset("v1.0-rc1+build", "a_b.tar.gz") || validAsset("..", "a") || validAsset("v1", "..") || validAsset("v1", "../a") || validAsset("v1/x", "a") || validAsset("v1", ".hidden") || validAsset("", "a") {
		t.Error("validAsset")
	}
}
