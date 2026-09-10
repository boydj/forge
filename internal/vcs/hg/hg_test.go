package hg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"as215520.net/forge/internal/vcs"
)

func newBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := New(Options{})
	if err != nil {
		t.Skip("hg not available:", err)
	}
	return b
}

// fixture holds the nodes of the scratch history:
//
//	c0 initial (default) -- c1 print hi -- c2 rename readme -- c3 tag v1.0
//	                          \ bookmark bm1                    \
//	                                                    c4 feature work (branch feature) -- c5 add link
type fixture struct {
	path string
	c    []vcs.RevisionID
}

func makeFixture(t *testing.T, b *Backend) fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repos", "alice", "proj.hg")
	if err := b.Init(ctx, repo, "default"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(b.opts.Binary, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "HGPLAIN=1", "HGRCPATH=/dev/null", "HGUSER=Alice <alice@example.org>")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hg %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(work, name)), 0o755)
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(work, 0o755)
	run("init")
	day := 2
	commit := func(msg string) vcs.RevisionID {
		t.Helper()
		run("commit", "-q", "-A", "-d", "2026-01-0"+string(rune('0'+day))+" 03:04:05 +0000", "-m", msg)
		day++
		return vcs.RevisionID(run("log", "-r", ".", "-T", "{node}"))
	}
	var f fixture
	f.path = repo
	write("README.md", "# proj\n\nhello\n")
	write("src/main.go", "package main\n\nfunc main() {}\n")
	_ = os.WriteFile(filepath.Join(work, "bin.dat"), []byte{0, 1, 2, 3, 0xff}, 0o644)
	f.c = append(f.c, commit("initial commit\n\nWith a body."))
	write("src/main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n")
	_ = os.Chmod(filepath.Join(work, "src", "main.go"), 0o755)
	f.c = append(f.c, commit("print hi"))
	run("bookmark", "-r", ".", "bm1")
	run("mv", "-q", "README.md", "README")
	f.c = append(f.c, commit("rename readme"))
	run("tag", "-d", "2026-01-05 00:00:00 +0000", "-m", "first release", "v1.0")
	f.c = append(f.c, vcs.RevisionID(run("log", "-r", ".", "-T", "{node}")))
	day++
	run("branch", "-q", "feature")
	write("feature.txt", "f\n")
	f.c = append(f.c, commit("feature work"))
	if err := os.Symlink("README", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	f.c = append(f.c, commit("add link"))
	run("push", "-q", "-f", "--new-branch", repo)
	run("-R", repo, "bookmark", "-r", string(f.c[1]), "bm1")
	return f
}

func open(t *testing.T, b *Backend, path string) vcs.Repository {
	t.Helper()
	repo, err := b.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestBackendVersion(t *testing.T) {
	b := newBackend(t)
	v := b.Version(context.Background())
	if v == "unknown" || v == "" {
		t.Fatalf("version = %q", v)
	}
	t.Logf("hg %s at %s", v, b.opts.Binary)
	if b.Name() != "hg" {
		t.Fatal("name")
	}
}

func TestEmptyRepo(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.hg")
	if err := b.Init(ctx, path, "default"); err != nil {
		t.Fatal(err)
	}
	if err := b.Init(ctx, path, "default"); err == nil {
		t.Fatal("second init should fail")
	}
	repo := open(t, b, path)
	if empty, err := repo.Empty(ctx); err != nil || !empty {
		t.Fatalf("empty = %v, %v", empty, err)
	}
	if db, _ := repo.DefaultBranch(ctx); db != "default" {
		t.Fatalf("default branch = %q", db)
	}
	if refs, err := repo.Refs(ctx); err != nil || len(refs) != 0 {
		t.Fatalf("refs = %v, %v", refs, err)
	}
	if _, err := repo.Resolve(ctx, "default"); err != vcs.ErrNotFound {
		t.Fatalf("resolve on empty: %v", err)
	}
	if err := b.SetDefaultBranch(ctx, path, "nope"); err != vcs.ErrNotFound {
		t.Fatalf("set missing default branch: %v", err)
	}
	if _, err := b.Open(filepath.Join(t.TempDir(), "missing")); err != vcs.ErrNotFound {
		t.Fatalf("open missing: %v", err)
	}
	if err := repo.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRefsAndResolve(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	if empty, _ := repo.Empty(ctx); empty {
		t.Fatal("repo should not be empty")
	}
	refs, err := repo.Refs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]vcs.Ref{}
	for _, r := range refs {
		got[r.Name] = r
		if r.When.IsZero() {
			t.Errorf("%s: zero date", r.Name)
		}
	}
	if _, ok := got["tip"]; ok {
		t.Error("tip must not be listed")
	}
	if r := got["default"]; r.Kind != vcs.RefBranch || r.Target != f.c[3] {
		t.Errorf("default = %+v", r)
	}
	if r := got["feature"]; r.Kind != vcs.RefBranch || r.Target != f.c[5] {
		t.Errorf("feature = %+v", r)
	}
	if r := got["bm1"]; r.Kind != vcs.RefBranch || r.Target != f.c[1] {
		t.Errorf("bm1 = %+v", r)
	}
	if r := got["v1.0"]; r.Kind != vcs.RefTag || r.Target != f.c[2] {
		t.Errorf("v1.0 = %+v", r)
	}
	if len(refs) != 4 {
		t.Errorf("want 4 refs, got %d: %+v", len(refs), refs)
	}
	for name, want := range map[string]vcs.RevisionID{
		"default": f.c[3], "feature": f.c[5], "bm1": f.c[1], "v1.0": f.c[2],
		string(f.c[4]): f.c[4], string(f.c[4][:12]): f.c[4],
	} {
		id, err := repo.Resolve(ctx, name)
		if err != nil || id != want {
			t.Errorf("resolve %s = %s, %v; want %s", name, id, err, want)
		}
	}
	for _, name := range []string{"nope", "0000000000000000000000000000000000000001"} {
		if _, err := repo.Resolve(ctx, name); err != vcs.ErrNotFound {
			t.Errorf("resolve %s: %v", name, err)
		}
	}
	for _, name := range []string{"", "-x", "a\"b", "a:b", "x\x01"} {
		if _, err := repo.Resolve(ctx, name); err != vcs.ErrBadRef {
			t.Errorf("resolve %q: %v", name, err)
		}
	}
	if err := b.SetDefaultBranch(ctx, f.path, "feature"); err != nil {
		t.Fatal(err)
	}
	if db, _ := repo.DefaultBranch(ctx); db != "feature" {
		t.Fatalf("default branch = %q", db)
	}
	for _, id := range f.c {
		if typ, err := repo.ObjectType(ctx, id); err != nil || typ != "commit" {
			t.Errorf("objecttype %s = %s, %v", id, typ, err)
		}
	}
	if _, err := repo.ObjectType(ctx, "0000000000000000000000000000000000000001"); err != vcs.ErrNotFound {
		t.Errorf("objecttype unknown: %v", err)
	}
	if _, err := repo.ObjectType(ctx, "abc"); err != vcs.ErrBadRef {
		t.Errorf("objecttype short: %v", err)
	}
}

func TestRefsMatching(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	heads, err := repo.RefsMatching(ctx, "refs/heads/")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range heads {
		names[r.Name] = true
	}
	if len(heads) != 3 || !names["refs/heads/default"] || !names["refs/heads/feature"] || !names["refs/heads/bm1"] {
		t.Errorf("heads = %+v", heads)
	}
	one, err := repo.RefsMatching(ctx, "refs/heads/feature")
	if err != nil || len(one) != 1 || one[0].Target != f.c[5] {
		t.Errorf("single = %+v, %v", one, err)
	}
	tags, err := repo.RefsMatching(ctx, "refs/tags/")
	if err != nil || len(tags) != 1 || tags[0].Name != "refs/tags/v1.0" || tags[0].Kind != vcs.RefTag {
		t.Errorf("tags = %+v, %v", tags, err)
	}
	if _, err := repo.RefsMatching(ctx, "refs/changes/12/"); err != ErrUnsupported {
		t.Errorf("changes: %v", err)
	}
	if _, err := repo.RefsMatching(ctx, "heads/"); err != vcs.ErrBadRef {
		t.Errorf("bad prefix: %v", err)
	}
}

func TestRevisionAndLog(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	rev, err := repo.Revision(ctx, f.c[0])
	if err != nil {
		t.Fatal(err)
	}
	if rev.Subject != "initial commit" || rev.Body != "With a body." || len(rev.Parents) != 0 {
		t.Errorf("root = %+v", rev)
	}
	if rev.Author.Name != "Alice" || rev.Author.Email != "alice@example.org" || rev.Author.When.Year() != 2026 {
		t.Errorf("author = %+v", rev.Author)
	}
	if rev.Committer != rev.Author || rev.Tree != "default" {
		t.Errorf("committer/tree = %+v %q", rev.Committer, rev.Tree)
	}
	rev, err = repo.Revision(ctx, f.c[5])
	if err != nil || len(rev.Parents) != 1 || rev.Parents[0] != f.c[4] || rev.Tree != "feature" {
		t.Errorf("tip = %+v, %v", rev, err)
	}
	if _, err := repo.Revision(ctx, "0000000000000000000000000000000000000001"); err != vcs.ErrNotFound {
		t.Errorf("unknown: %v", err)
	}
	log, err := repo.Log(ctx, f.c[5], vcs.LogOptions{})
	if err != nil || len(log) != 6 || log[0].ID != f.c[5] || log[5].ID != f.c[0] {
		t.Fatalf("log = %d, %v", len(log), err)
	}
	log, err = repo.Log(ctx, f.c[5], vcs.LogOptions{Limit: 2, Skip: 1})
	if err != nil || len(log) != 2 || log[0].ID != f.c[4] || log[1].ID != f.c[3] {
		t.Errorf("log skip = %+v, %v", log, err)
	}
	log, err = repo.Log(ctx, f.c[5], vcs.LogOptions{Path: "src/main.go"})
	if err != nil || len(log) != 2 || log[0].ID != f.c[1] || log[1].ID != f.c[0] {
		t.Errorf("log path = %d, %v", len(log), err)
	}
	log, err = repo.Log(ctx, f.c[5], vcs.LogOptions{Path: "src"})
	if err != nil || len(log) != 2 {
		t.Errorf("log dir = %d, %v", len(log), err)
	}
	if _, err := repo.Log(ctx, f.c[5], vcs.LogOptions{Path: "../x"}); err != vcs.ErrBadPath {
		t.Errorf("log bad path: %v", err)
	}
	if _, err := repo.Log(ctx, "0000000000000000000000000000000000000001", vcs.LogOptions{}); err != vcs.ErrNotFound {
		t.Errorf("log unknown: %v", err)
	}
}

func TestTreeAndBlob(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	root, err := repo.Tree(ctx, f.c[5], "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]vcs.TreeEntry{}
	for _, e := range root {
		byName[e.Name] = e
	}
	if e := byName["src"]; e.Kind != vcs.EntryDir || e.Size != -1 {
		t.Errorf("src = %+v", e)
	}
	if e := byName["README"]; e.Kind != vcs.EntryFile || e.Size != 15 || e.Mode != "100644" || len(e.ID) != 40 {
		t.Errorf("README = %+v", e)
	}
	if e := byName["link"]; e.Kind != vcs.EntrySymlink || e.Mode != "120000" {
		t.Errorf("link = %+v", e)
	}
	if e := byName["bin.dat"]; e.Size != 5 {
		t.Errorf("bin.dat = %+v", e)
	}
	if _, ok := byName["README.md"]; ok {
		t.Error("renamed file still present")
	}
	if root[0].Name != "src" {
		t.Errorf("directories should sort first: %+v", root)
	}
	src, err := repo.Tree(ctx, f.c[5], "src")
	if err != nil || len(src) != 1 || src[0].Name != "main.go" || src[0].Kind != vcs.EntryExecutable || src[0].Mode != "100755" {
		t.Errorf("src tree = %+v, %v", src, err)
	}
	if _, err := repo.Tree(ctx, f.c[5], "src/main.go"); err != vcs.ErrNotDir {
		t.Errorf("tree on file: %v", err)
	}
	if _, err := repo.Tree(ctx, f.c[5], "nope"); err != vcs.ErrNotFound {
		t.Errorf("tree missing: %v", err)
	}
	if _, err := repo.Tree(ctx, "0000000000000000000000000000000000000001", ""); err != vcs.ErrNotFound {
		t.Errorf("tree unknown rev: %v", err)
	}
	if _, err := repo.Tree(ctx, f.c[5], "/etc"); err != vcs.ErrBadPath {
		t.Errorf("tree bad path: %v", err)
	}
	// The first revision still has README.md.
	old, err := repo.Tree(ctx, f.c[0], "")
	if err != nil || len(old) != 3 {
		t.Errorf("old tree = %+v, %v", old, err)
	}

	blob, err := repo.Blob(ctx, f.c[5], "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(blob.Reader)
	_ = blob.Close()
	if err != nil || !strings.Contains(string(data), "fmt.Println") || blob.Size != int64(len(data)) {
		t.Errorf("blob = %q (%d), %v", data, blob.Size, err)
	}
	blob, err = repo.Blob(ctx, f.c[5], "bin.dat")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(blob.Reader)
	_ = blob.Close()
	if !bytes.Equal(data, []byte{0, 1, 2, 3, 0xff}) {
		t.Errorf("binary blob = %v", data)
	}
	if _, err := repo.Blob(ctx, f.c[5], "src"); err != vcs.ErrIsDir {
		t.Errorf("blob on dir: %v", err)
	}
	if _, err := repo.Blob(ctx, f.c[5], ""); err != vcs.ErrIsDir {
		t.Errorf("blob on root: %v", err)
	}
	if _, err := repo.Blob(ctx, f.c[5], "nope.txt"); err != vcs.ErrNotFound {
		t.Errorf("blob missing: %v", err)
	}
	if _, err := repo.Blob(ctx, f.c[5], "README.md"); err != vcs.ErrNotFound {
		t.Errorf("blob renamed-away: %v", err)
	}
	if _, err := repo.Blob(ctx, "0000000000000000000000000000000000000001", "README"); err != vcs.ErrNotFound {
		t.Errorf("blob unknown rev: %v", err)
	}
}

func TestDiff(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	d, err := repo.Diff(ctx, f.c[2], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stats) != 1 || d.Stats[0].Path != "README" || d.Stats[0].OldPath != "README.md" || d.Stats[0].Added != 0 {
		t.Errorf("rename stats = %+v", d.Stats)
	}
	if !strings.Contains(d.Patch, "rename from README.md") || d.Truncated {
		t.Errorf("rename patch = %q", d.Patch)
	}
	d, err = repo.Diff(ctx, f.c[1], 0)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "src/main.go" || d.Stats[0].Added != 3 || d.Stats[0].Deleted != 1 {
		t.Errorf("edit stats = %+v, %v", d.Stats, err)
	}
	if !strings.Contains(d.Patch, "old mode 100644") || !strings.Contains(d.Patch, "new mode 100755") {
		t.Errorf("mode change missing: %q", d.Patch)
	}
	d, err = repo.Diff(ctx, f.c[0], 0)
	if err != nil || len(d.Stats) != 3 {
		t.Fatalf("root diff = %+v, %v", d, err)
	}
	for _, s := range d.Stats {
		if s.Path == "bin.dat" && !s.Binary {
			t.Errorf("bin.dat not binary: %+v", s)
		}
		if s.Path == "README.md" && s.Added != 3 {
			t.Errorf("README.md = %+v", s)
		}
	}
	d, err = repo.Diff(ctx, f.c[0], 100)
	if err != nil || !d.Truncated || len(d.Patch) > 100 || len(d.Stats) != 3 || !strings.HasSuffix(d.Patch, "\n") {
		t.Errorf("truncated = %v %d %d, %v", d.Truncated, len(d.Patch), len(d.Stats), err)
	}
	if _, err := repo.Diff(ctx, "0000000000000000000000000000000000000001", 0); err != vcs.ErrNotFound {
		t.Errorf("diff unknown: %v", err)
	}
	d, err = repo.DiffRange(ctx, f.c[1], f.c[5], 0)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]vcs.DiffStat{}
	for _, s := range d.Stats {
		paths[s.Path] = s
	}
	if len(d.Stats) != 4 || paths["README"].OldPath != "README.md" || paths["feature.txt"].Added != 1 || paths["link"].Added != 1 {
		t.Errorf("range stats = %+v", d.Stats)
	}
	d, err = repo.DiffPath(ctx, f.c[1], f.c[5], "feature.txt", 0)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "feature.txt" {
		t.Errorf("diff path = %+v, %v", d, err)
	}
	d, err = repo.DiffPath(ctx, f.c[1], f.c[5], "src", 0)
	if err != nil || len(d.Stats) != 0 {
		t.Errorf("diff untouched dir = %+v, %v", d, err)
	}
	if _, err := repo.DiffRange(ctx, f.c[1], "0000000000000000000000000000000000000001", 0); err != vcs.ErrNotFound {
		t.Errorf("range unknown: %v", err)
	}
	if _, err := repo.DiffRange(ctx, "abc", f.c[1], 0); err != vcs.ErrBadRef {
		t.Errorf("range bad id: %v", err)
	}
}

func TestPlumbing(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	unknown := vcs.RevisionID("0000000000000000000000000000000000000001")

	if mb, err := repo.MergeBase(ctx, f.c[1], f.c[5]); err != nil || mb != f.c[1] {
		t.Errorf("merge-base = %s, %v", mb, err)
	}
	if mb, err := repo.MergeBase(ctx, f.c[3], f.c[4]); err != nil || mb != f.c[3] {
		t.Errorf("merge-base linear = %s, %v", mb, err)
	}
	if _, err := repo.MergeBase(ctx, f.c[1], unknown); err != vcs.ErrNotFound {
		t.Errorf("merge-base unknown: %v", err)
	}
	if ok, err := repo.IsAncestor(ctx, f.c[1], f.c[5]); err != nil || !ok {
		t.Errorf("is-ancestor = %v, %v", ok, err)
	}
	if ok, err := repo.IsAncestor(ctx, f.c[5], f.c[1]); err != nil || ok {
		t.Errorf("is-ancestor reverse = %v, %v", ok, err)
	}
	if ok, err := repo.IsAncestor(ctx, f.c[2], f.c[2]); err != nil || !ok {
		t.Errorf("is-ancestor self = %v, %v", ok, err)
	}
	if _, err := repo.IsAncestor(ctx, unknown, f.c[1]); err != vcs.ErrNotFound {
		t.Errorf("is-ancestor unknown: %v", err)
	}
	if n, err := repo.CountCommits(ctx, f.c[1], f.c[5]); err != nil || n != 4 {
		t.Errorf("count = %d, %v", n, err)
	}
	if n, err := repo.CountCommits(ctx, f.c[5], f.c[1]); err != nil || n != 0 {
		t.Errorf("count reverse = %d, %v", n, err)
	}
	if _, err := repo.CountCommits(ctx, unknown, f.c[5]); err != vcs.ErrNotFound {
		t.Errorf("count unknown: %v", err)
	}
	list, err := repo.ListCommits(ctx, f.c[1], f.c[5], 0)
	if err != nil || len(list) != 4 || list[0].ID != f.c[2] || list[3].ID != f.c[5] {
		t.Errorf("list = %d, %v", len(list), err)
	}
	list, err = repo.ListCommits(ctx, f.c[1], f.c[5], 2)
	if err != nil || len(list) != 2 || list[0].ID != f.c[4] || list[1].ID != f.c[5] {
		t.Errorf("list limited = %+v, %v", list, err)
	}

	var buf bytes.Buffer
	if err := repo.FormatPatch(ctx, f.c[1], f.c[5], 0, &buf); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "# HG changeset patch"); n != 4 {
		t.Errorf("format-patch: %d changesets\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "diff --git a/feature.txt b/feature.txt") {
		t.Error("format-patch not in git format")
	}
	buf.Reset()
	if err := repo.FormatPatch(ctx, f.c[1], f.c[5], 64, &buf); err != vcs.ErrTooLarge || buf.Len() != 64 {
		t.Errorf("format-patch limit: %v, %d", err, buf.Len())
	}
	if err := repo.FormatPatch(ctx, unknown, f.c[5], 0, io.Discard); err != vcs.ErrNotFound {
		t.Errorf("format-patch unknown: %v", err)
	}

	if _, _, err := repo.RangeDiff(ctx, f.c[1], f.c[3], f.c[1], f.c[5], 0); err != ErrUnsupported {
		t.Errorf("range-diff: %v", err)
	}
	if _, _, err := repo.MergeTree(ctx, f.c[3], f.c[5]); err != ErrUnsupported {
		t.Errorf("merge-tree: %v", err)
	}
	if _, err := repo.CommitTree(ctx, "x", nil, vcs.Signature{}, vcs.Signature{}, "m"); err != ErrUnsupported {
		t.Errorf("commit-tree: %v", err)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "refs/heads/x", New: f.c[1]}}, "r"); err != ErrUnsupported {
		t.Errorf("update-refs: %v", err)
	}
	if err := repo.UpdateRefs(ctx, nil, "r"); err != nil {
		t.Errorf("update-refs empty: %v", err)
	}
}

func TestSizeCheckFetch(t *testing.T) {
	b := newBackend(t)
	f := makeFixture(t, b)
	ctx := context.Background()
	repo := open(t, b, f.path)
	if n, err := repo.Size(ctx); err != nil || n <= 0 {
		t.Errorf("size = %d, %v", n, err)
	}
	if err := repo.Check(ctx); err != nil {
		t.Errorf("check: %v", err)
	}
	mirror := filepath.Join(t.TempDir(), "mirror.hg")
	if err := b.Init(ctx, mirror, "default"); err != nil {
		t.Fatal(err)
	}
	if err := b.Fetch(ctx, mirror, "-bad"); err != vcs.ErrBadPath {
		t.Errorf("fetch option injection: %v", err)
	}
	if err := b.Fetch(ctx, mirror, f.path); err != nil {
		t.Fatal(err)
	}
	m := open(t, b, mirror)
	refs, err := m.Refs(ctx)
	if err != nil || len(refs) != 4 {
		t.Errorf("mirror refs = %+v, %v", refs, err)
	}
	if id, err := m.Resolve(ctx, "bm1"); err != nil || id != f.c[1] {
		t.Errorf("mirror bookmark = %s, %v", id, err)
	}
	// Corrupt the store and expect Check to complain.
	entries, _ := filepath.Glob(filepath.Join(mirror, ".hg", "store", "data", "*", "*.i"))
	if len(entries) == 0 {
		entries, _ = filepath.Glob(filepath.Join(mirror, ".hg", "store", "data", "*.i"))
	}
	if len(entries) > 0 {
		if err := os.WriteFile(entries[0], []byte("garbage"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := m.Check(ctx); err == nil {
			t.Error("check should fail on a corrupted store")
		}
	}
}

func TestErrorType(t *testing.T) {
	b := newBackend(t)
	_, err := b.runIn(context.Background(), "", "log", "-R", "/nonexistent")
	var he *Error
	if !errors.As(err, &he) || he.ExitCode() != 255 || he.Stderr == "" {
		t.Fatalf("err = %v", err)
	}
}
