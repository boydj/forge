package mirror

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// setup creates <repos>/a/b.git with one commit on main and returns the
// repos dir, a git backend and a store.
func setup(t *testing.T) (string, *gitvcs.Backend, *store.Store) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	repos := filepath.Join(dir, "repos")
	src := filepath.Join(repos, "a", "b.git")
	_ = os.MkdirAll(src, 0o755)
	git(t, src, "init", "-q", "--bare", "-b", "main")
	work := filepath.Join(dir, "work")
	_ = os.MkdirAll(work, 0o755)
	git(t, work, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "f"), []byte("x\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "one")
	git(t, work, "tag", "v1")
	git(t, work, "push", "-q", src, "main", "v1")
	g, err := gitvcs.New(gitvcs.Options{HomeDir: dir, Timeout: 30 * time.Second})
	if err != nil {
		t.Skip(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "forge.db"), "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return repos, g, st
}

func TestPushMirrorsRefs(t *testing.T) {
	repos, g, st := setup(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, t.TempDir(), "init", "-q", "--bare", remote)
	reg := metrics.New()
	w := New(Options{Targets: map[string]string{"a/b": remote}, ReposDir: repos, Git: g, Store: st, Metrics: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if !w.Enabled() {
		t.Fatalf("disabled: %s", w.Reason())
	}
	ctx := context.Background()
	if err := w.Push(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	refs := git(t, t.TempDir(), "--git-dir="+remote, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/heads/main") || !strings.Contains(refs, "refs/tags/v1") {
		t.Fatalf("remote refs after mirror:\n%s", refs)
	}
	if v, _ := st.Setting(ctx, settingKey("a/b")); v == "" {
		t.Error("last success not recorded")
	}
	// A second worker restores the gauge from the setting.
	w2 := New(Options{Targets: map[string]string{"a/b": remote}, ReposDir: repos, Git: g, Store: st, Metrics: metrics.New(), Log: w.o.Log})
	w2.loadState(ctx)
	// --mirror also deletes: remove the tag locally, push again, gone remotely.
	git(t, t.TempDir(), "--git-dir="+filepath.Join(repos, "a", "b.git"), "tag", "-d", "v1")
	if err := w.Push(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if refs := git(t, t.TempDir(), "--git-dir="+remote, "for-each-ref"); strings.Contains(refs, "v1") {
		t.Fatalf("tag not deleted on the mirror:\n%s", refs)
	}
	if err := w.Push(ctx, "nobody/nothing"); !errors.Is(err, ErrNoMirror) {
		t.Errorf("unknown repo: %v", err)
	}
}

func TestRetryAndLeadership(t *testing.T) {
	repos, g, st := setup(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	missing := filepath.Join(t.TempDir(), "missing.git")
	w := New(Options{Targets: map[string]string{"a/b": missing}, ReposDir: repos, Git: g, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock,
		Backoff: []time.Duration{time.Minute, 5 * time.Minute}})
	w.OnPush(&store.Repo{Owner: "a", Name: "b"})
	w.OnPush(&store.Repo{Owner: "a", Name: "b"}) // coalesces
	w.OnPush(&store.Repo{Owner: "x", Name: "y"}) // no mirror: ignored
	if _, due, ok := w.Pending("a/b"); !ok || !due.Equal(now.Add(2*time.Second)) {
		t.Fatalf("debounced entry: ok=%v due=%v", ok, due)
	}
	if _, _, ok := w.Pending("x/y"); ok {
		t.Fatal("unmirrored repository queued")
	}
	w.RunDue(context.Background(), now) // not due yet
	if a, _, ok := w.Pending("a/b"); !ok || a != 0 {
		t.Fatalf("ran early: ok=%v attempts=%d", ok, a)
	}
	now = now.Add(3 * time.Second)
	w.RunDue(context.Background(), now) // fails: remote missing
	a, due, ok := w.Pending("a/b")
	if !ok || a != 1 || !due.Equal(now.Add(time.Minute)) {
		t.Fatalf("after first failure: ok=%v attempts=%d due=%v", ok, a, due)
	}
	now = due
	w.RunDue(context.Background(), now)
	if a, due, _ = w.Pending("a/b"); a != 2 || !due.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("after second failure: attempts=%d due=%v", a, due)
	}
	now = due
	w.RunDue(context.Background(), now)
	if a, due, _ = w.Pending("a/b"); a != 3 || !due.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("backoff should cap at the last delay: attempts=%d due=%v", a, due)
	}

	// A replica never pushes.
	replica := New(Options{Targets: map[string]string{"a/b": missing}, ReposDir: repos, Git: g, Log: w.o.Log, Leads: func(context.Context, string) (bool, error) { return false, nil }})
	if err := replica.Push(context.Background(), "a/b"); !errors.Is(err, ErrNotLeader) {
		t.Errorf("replica push: %v", err)
	}
	// ssh mirror without a key: disabled, not an error.
	off := New(Options{Targets: map[string]string{"a/b": "git@github.com:x/y.git"}, ReposDir: repos, Git: g, Log: w.o.Log})
	if off.Enabled() || off.Reason() == "" {
		t.Errorf("ssh mirror without key should be disabled")
	}
	off.OnPush(&store.Repo{Owner: "a", Name: "b"})
	if _, _, ok := off.Pending("a/b"); ok {
		t.Error("disabled worker queued a push")
	}
}

func TestEnvOverrides(t *testing.T) {
	_, g, _ := setup(t)
	w := New(Options{Targets: map[string]string{"a/b": "git@github.com:x/y.git"}, KeyFile: "/dev/null", KnownHostsFile: "/etc/kh", Git: g, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	env := w.env("git@github.com:x/y.git")
	joined := strings.Join(env, "\n")
	base := g.Env()
	var n int
	for _, e := range base {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			n, _ = strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT="))
		}
	}
	if !strings.Contains(joined, "GIT_CONFIG_COUNT="+strconv.Itoa(n+1)+"\n") || !strings.Contains(joined, "GIT_CONFIG_KEY_"+strconv.Itoa(n)+"=protocol.ssh.allow\nGIT_CONFIG_VALUE_"+strconv.Itoa(n)+"=always") {
		t.Errorf("config override not appended/renumbered:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_SSH_COMMAND=ssh -i /dev/null -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/etc/kh -o BatchMode=yes") {
		t.Errorf("no GIT_SSH_COMMAND:\n%s", joined)
	}
	if got := Redact("https://user:tok@example.org/x.git"); got != "https://user@example.org/x.git" {
		t.Errorf("redact: %s", got)
	}
}
