package git

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"as215520.net/forge/internal/vcs"
)

// makeFixture creates a bare repo with a small history pushed from a
// scratch working copy and returns the bare path.
func makeFixture(t *testing.T, b *Backend) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	bare := filepath.Join(dir, "repos", "alice", "proj.git")
	if err := b.Init(ctx, bare, "main"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.org",
			"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.org",
			"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	_ = os.MkdirAll(work, 0o755)
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# proj\n\nhello\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(work, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "bin.dat"), []byte{0, 1, 2, 3, 0xff}, 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "initial commit\n\nWith a body.")
	_ = os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n"), 0o644)
	run("commit", "-q", "-am", "print hi")
	run("mv", "README.md", "README")
	run("commit", "-q", "-m", "rename readme")
	run("tag", "-a", "v1.0", "-m", "first release")
	run("tag", "light")
	run("checkout", "-q", "-b", "feature")
	_ = os.WriteFile(filepath.Join(work, "feature.txt"), []byte("f\n"), 0o644)
	run("add", "feature.txt")
	run("commit", "-q", "-m", "feature work")
	run("push", "-q", "--all", bare)
	run("push", "-q", "--tags", bare)
	return bare
}

func newBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := New(Options{HomeDir: t.TempDir(), HooksDir: t.TempDir()})
	if err != nil {
		t.Skip("git not available:", err)
	}
	return b
}

func TestRepoRead(t *testing.T) {
	b := newBackend(t)
	bare := makeFixture(t, b)
	ctx := context.Background()
	repo, err := b.Open(bare)
	if err != nil {
		t.Fatal(err)
	}
	if empty, _ := repo.Empty(ctx); empty {
		t.Fatal("repo should not be empty")
	}
	if db, _ := repo.DefaultBranch(ctx); db != "main" {
		t.Errorf("default branch %q", db)
	}
	refs, err := repo.Refs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]vcs.Ref{}
	for _, r := range refs {
		names[r.Name] = r
	}
	if names["main"].Kind != vcs.RefBranch || names["feature"].Kind != vcs.RefBranch {
		t.Errorf("branches: %+v", names)
	}
	if names["v1.0"].Kind != vcs.RefTag || names["v1.0"].Message != "first release" || names["v1.0"].Target == names["v1.0"].Object {
		t.Errorf("annotated tag: %+v", names["v1.0"])
	}
	if names["light"].Target != names["main"].Target {
		t.Errorf("lightweight tag should equal main: %+v", names["light"])
	}
	head, err := repo.Resolve(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Resolve(ctx, "--output=/tmp/x"); err == nil {
		t.Error("option injection accepted")
	}
	if _, err := repo.Resolve(ctx, "nope"); err != vcs.ErrNotFound {
		t.Errorf("missing ref: %v", err)
	}
	rev, err := repo.Revision(ctx, head)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Subject != "rename readme" || rev.Author.Name != "Alice" || len(rev.Parents) != 1 || rev.Author.When.Year() != 2026 {
		t.Errorf("revision: %+v", rev)
	}
	log, err := repo.Log(ctx, head, vcs.LogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 || log[2].Subject != "initial commit" || log[2].Body != "With a body." {
		t.Errorf("log: %d entries, last %+v", len(log), log[len(log)-1])
	}
	log, _ = repo.Log(ctx, head, vcs.LogOptions{Path: "src/main.go", Limit: 10})
	if len(log) != 2 {
		t.Errorf("path log: %d", len(log))
	}
	entries, err := repo.Tree(ctx, head, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]vcs.TreeEntry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if got["src"].Kind != vcs.EntryDir || got["README"].Kind != vcs.EntryFile || got["README"].Size != 14 {
		t.Errorf("tree: %+v", got)
	}
	sub, err := repo.Tree(ctx, head, "src")
	if err != nil || len(sub) != 1 || sub[0].Name != "main.go" {
		t.Errorf("subtree: %v %+v", err, sub)
	}
	if _, err := repo.Tree(ctx, head, "README"); err != vcs.ErrNotDir {
		t.Errorf("tree on file: %v", err)
	}
	if _, err := repo.Tree(ctx, head, "../x"); err != vcs.ErrBadPath {
		t.Errorf("traversal: %v", err)
	}
	blob, err := repo.Blob(ctx, head, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(blob.Reader)
	_ = blob.Close()
	if blob.Size != int64(len(data)) || !strings.Contains(string(data), "fmt.Println") {
		t.Errorf("blob: size=%d data=%q", blob.Size, data)
	}
	if _, err := repo.Blob(ctx, head, "src"); err != vcs.ErrIsDir {
		t.Errorf("blob on dir: %v", err)
	}
	if _, err := repo.Blob(ctx, head, "missing"); err != vcs.ErrNotFound {
		t.Errorf("blob missing: %v", err)
	}
	d, err := repo.Diff(ctx, head, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stats) != 1 || d.Stats[0].OldPath != "README.md" || d.Stats[0].Path != "README" {
		t.Errorf("rename stat: %+v", d.Stats)
	}
	if !strings.Contains(d.Patch, "rename from README.md") {
		t.Errorf("patch: %q", d.Patch)
	}
	root := log[len(log)-1]
	d, err = repo.Diff(ctx, root.ID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stats) != 3 || !strings.Contains(d.Patch, "+package main") {
		t.Errorf("root diff: %+v", d.Stats)
	}
	var bin *vcs.DiffStat
	for i := range d.Stats {
		if d.Stats[i].Path == "bin.dat" {
			bin = &d.Stats[i]
		}
	}
	if bin == nil || !bin.Binary {
		t.Errorf("binary stat: %+v", d.Stats)
	}
	d, _ = repo.Diff(ctx, root.ID, 200)
	if !d.Truncated || len(d.Patch) > 200 {
		t.Errorf("truncation: %v %d", d.Truncated, len(d.Patch))
	}
	feat, _ := repo.Resolve(ctx, "feature")
	d, err = repo.DiffRange(ctx, head, feat, 1<<20)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "feature.txt" {
		t.Errorf("range diff: %v %+v", err, d)
	}
	if sz, err := repo.Size(ctx); err != nil || sz <= 0 {
		t.Errorf("size: %d %v", sz, err)
	}
	if err := repo.Check(ctx); err != nil {
		t.Errorf("check: %v", err)
	}
}

func TestInitAndEmpty(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "e.git")
	if err := b.Init(ctx, p, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p, "hooks")); !os.IsNotExist(err) {
		t.Error("hooks dir should be removed")
	}
	repo, _ := b.Open(p)
	if empty, err := repo.Empty(ctx); err != nil || !empty {
		t.Errorf("empty: %v %v", empty, err)
	}
	if err := b.Init(ctx, p, "main"); err == nil {
		t.Error("double init should fail")
	}
	if err := b.Init(ctx, filepath.Join(t.TempDir(), "x.git"), "--bad"); err == nil {
		t.Error("bad branch accepted")
	}
	if _, err := b.Open(t.TempDir()); err != vcs.ErrNotFound {
		t.Error("open of non-repo should fail")
	}
}

func TestFetchMirror(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	src := makeFixture(t, b)
	dst := filepath.Join(t.TempDir(), "replica.git")
	if err := b.Init(ctx, dst, "main"); err != nil {
		t.Fatal(err)
	}
	if err := b.Fetch(ctx, dst, src); err != nil {
		t.Fatal(err)
	}
	repo, _ := b.Open(dst)
	refs, _ := repo.Refs(ctx)
	if len(refs) != 4 {
		t.Errorf("replica refs: %d", len(refs))
	}
}

func TestCheckPath(t *testing.T) {
	for _, bad := range []string{"../a", "a/../../b", "/etc", "-x", "a\x00b", "./a", "a//b"} {
		if _, err := checkPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"", "a", "a/b.c", "a/b/", ".github/x"} {
		if _, err := checkPath(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}
