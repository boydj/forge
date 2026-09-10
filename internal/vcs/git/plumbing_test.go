package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"as215520.net/forge/internal/vcs"
)

// gitIn runs git in dir with a fixed identity (for building fixtures).
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.org",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.org",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPlumbingHistory(t *testing.T) {
	b := newBackend(t)
	bare := makeFixture(t, b)
	ctx := context.Background()
	rp, err := b.Open(bare)
	if err != nil {
		t.Fatal(err)
	}
	repo := rp.(*Repo)
	main, _ := repo.Resolve(ctx, "main")
	feat, _ := repo.Resolve(ctx, "feature")

	mb, err := repo.MergeBase(ctx, main, feat)
	if err != nil || mb != main {
		t.Errorf("merge-base: %v %v (want %v)", mb, err, main)
	}
	if _, err := repo.MergeBase(ctx, main, "nope"); err != vcs.ErrNotFound {
		t.Errorf("merge-base missing: %v", err)
	}
	if _, err := repo.MergeBase(ctx, main, "--all"); err != vcs.ErrBadRef {
		t.Errorf("merge-base injection: %v", err)
	}
	if ok, err := repo.IsAncestor(ctx, main, feat); err != nil || !ok {
		t.Errorf("main ancestor of feature: %v %v", ok, err)
	}
	if ok, err := repo.IsAncestor(ctx, feat, main); err != nil || ok {
		t.Errorf("feature ancestor of main: %v %v", ok, err)
	}
	if ok, err := repo.IsAncestor(ctx, main, main); err != nil || !ok {
		t.Errorf("self ancestor: %v %v", ok, err)
	}
	if _, err := repo.IsAncestor(ctx, main, "nope"); err != vcs.ErrNotFound {
		t.Errorf("is-ancestor missing: %v", err)
	}
	if n, err := repo.CountCommits(ctx, main, feat); err != nil || n != 1 {
		t.Errorf("count main..feature: %d %v", n, err)
	}
	root := mustRoot(t, repo, main)
	if n, err := repo.CountCommits(ctx, root, feat); err != nil || n != 3 {
		t.Errorf("count root..feature: %d %v", n, err)
	}
	revs, err := repo.ListCommits(ctx, root, feat, 0)
	if err != nil || len(revs) != 3 || revs[0].Subject != "print hi" || revs[2].Subject != "feature work" {
		t.Errorf("list commits: %v %+v", err, revs)
	}
	if revs, _ := repo.ListCommits(ctx, root, feat, 2); len(revs) != 2 {
		t.Errorf("list limit: %d", len(revs))
	}
	if revs, err := repo.ListCommits(ctx, feat, feat, 0); err != nil || len(revs) != 0 {
		t.Errorf("empty range: %v %d", err, len(revs))
	}
	if _, err := repo.ListCommits(ctx, root, "nope", 0); err != vcs.ErrNotFound {
		t.Errorf("list missing: %v", err)
	}

	// diff-path
	d, err := repo.DiffPath(ctx, root, feat, "feature.txt", 1<<20)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "feature.txt" || !strings.Contains(d.Patch, "+f") {
		t.Errorf("diff-path: %v %+v", err, d)
	}
	d, err = repo.DiffPath(ctx, root, feat, "src", 1<<20)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "src/main.go" {
		t.Errorf("diff-path dir: %v %+v", err, d)
	}
	if d, err := repo.DiffPath(ctx, root, feat, "", 1<<20); err != nil || len(d.Stats) != 3 {
		t.Errorf("diff-path unfiltered: %v %+v", err, d)
	}
	if _, err := repo.DiffPath(ctx, root, feat, "../x", 1<<20); err != vcs.ErrBadPath {
		t.Errorf("diff-path traversal: %v", err)
	}

	// object types
	if ty, err := repo.ObjectType(ctx, main); err != nil || ty != "commit" {
		t.Errorf("type commit: %q %v", ty, err)
	}
	refs, _ := repo.Refs(ctx)
	for _, r := range refs {
		if r.Name == "v1.0" {
			if ty, _ := repo.ObjectType(ctx, r.Object); ty != "tag" {
				t.Errorf("type tag: %q", ty)
			}
		}
	}
	rev, _ := repo.Revision(ctx, main)
	if ty, _ := repo.ObjectType(ctx, vcs.RevisionID(rev.Tree)); ty != "tree" {
		t.Errorf("type tree: %q", ty)
	}
	if _, err := repo.ObjectType(ctx, "deadbeef"); err != vcs.ErrNotFound {
		t.Errorf("type missing: %v", err)
	}
}

func mustRoot(t *testing.T, repo *Repo, id vcs.RevisionID) vcs.RevisionID {
	t.Helper()
	log, err := repo.Log(context.Background(), id, vcs.LogOptions{Limit: 100})
	if err != nil || len(log) == 0 {
		t.Fatal("log:", err)
	}
	return log[len(log)-1].ID
}

func TestRangeDiffAndFormatPatch(t *testing.T) {
	b := newBackend(t)
	bare := makeFixture(t, b)
	ctx := context.Background()
	rp, _ := b.Open(bare)
	repo := rp.(*Repo)
	main, _ := repo.Resolve(ctx, "main")

	// Build two versions of a change on top of main: v1 adds notes.txt,
	// v2 is the same commit amended (one more line, new subject). The
	// commit is large enough for range-diff to pair the two versions.
	work := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(work), "clone", "-q", "-b", "main", bare, work)
	lines := strings.Repeat("line\n", 20)
	_ = os.WriteFile(filepath.Join(work, "notes.txt"), []byte(lines), 0o644)
	gitIn(t, work, "add", "notes.txt")
	gitIn(t, work, "commit", "-q", "-m", "add notes")
	gitIn(t, work, "push", "-q", bare, "HEAD:refs/heads/change-v1")
	_ = os.WriteFile(filepath.Join(work, "notes.txt"), []byte(lines+"extra\n"), 0o644)
	gitIn(t, work, "commit", "-q", "--amend", "-a", "-m", "add notes v2")
	gitIn(t, work, "push", "-q", bare, "HEAD:refs/heads/change-v2")
	feat, err := repo.Resolve(ctx, "change-v1")
	if err != nil {
		t.Fatal(err)
	}
	feat2, err := repo.Resolve(ctx, "change-v2")
	if err != nil {
		t.Fatal(err)
	}

	text, trunc, err := repo.RangeDiff(ctx, main, feat, main, feat2, 1<<20)
	if err != nil || trunc {
		t.Fatalf("range-diff: %v %v", err, trunc)
	}
	if !strings.Contains(text, "1:") || !strings.Contains(text, " ! 1:") || !strings.Contains(text, "add notes v2") || !strings.Contains(text, "++extra") {
		t.Errorf("range-diff text:\n%s", text)
	}
	if _, trunc, err := repo.RangeDiff(ctx, main, feat, main, feat2, 40); err != nil || !trunc {
		t.Errorf("range-diff truncation: %v %v", err, trunc)
	}
	if _, _, err := repo.RangeDiff(ctx, main, feat, main, "nope", 0); err != vcs.ErrNotFound {
		t.Errorf("range-diff missing: %v", err)
	}

	var mbox bytes.Buffer
	root := mustRoot(t, repo, main)
	if err := repo.FormatPatch(ctx, root, feat2, 0, &mbox); err != nil {
		t.Fatal("format-patch:", err)
	}
	if n := strings.Count(mbox.String(), "\nFrom "); n+1 != 3 || !strings.HasPrefix(mbox.String(), "From ") {
		// root..feat2 = "print hi", "rename readme", "add notes v2"
		t.Errorf("mbox: %d From lines\n%s", n+1, mbox.String())
	}
	// Apply the mbox with git am onto the root commit in a scratch clone.
	scratch := filepath.Join(t.TempDir(), "am")
	gitIn(t, filepath.Dir(scratch), "clone", "-q", bare, scratch)
	gitIn(t, scratch, "checkout", "-q", "-b", "replay", string(root))
	patch := filepath.Join(t.TempDir(), "series.mbox")
	if err := os.WriteFile(patch, mbox.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, scratch, "am", "-q", patch)
	if got := gitIn(t, scratch, "rev-parse", "HEAD^{tree}"); got != gitIn(t, scratch, "rev-parse", string(feat2)+"^{tree}") {
		t.Errorf("git am tree mismatch: %s", got)
	}
	if got := gitIn(t, scratch, "log", "-1", "--format=%s"); got != "add notes v2" {
		t.Errorf("git am subject: %q", got)
	}
	var small bytes.Buffer
	if err := repo.FormatPatch(ctx, root, feat2, 100, &small); err != vcs.ErrTooLarge || small.Len() != 100 {
		t.Errorf("format-patch limit: %v %d", err, small.Len())
	}
	if err := repo.FormatPatch(ctx, root, "nope", 0, &small); err != vcs.ErrNotFound {
		t.Errorf("format-patch missing: %v", err)
	}
	if err := repo.FormatPatch(ctx, feat2, feat2, 0, &small); err != nil {
		t.Errorf("format-patch empty range: %v", err)
	}
}

func TestMergeTreeCommitTreeUpdateRefs(t *testing.T) {
	b := newBackend(t)
	bare := makeFixture(t, b)
	ctx := context.Background()
	rp, _ := b.Open(bare)
	repo := rp.(*Repo)
	main, _ := repo.Resolve(ctx, "main")
	feat, _ := repo.Resolve(ctx, "feature")

	// Clean merge of feature into main.
	tree, conflicts, err := repo.MergeTree(ctx, main, feat)
	if err != nil || len(conflicts) != 0 || len(tree) < 40 {
		t.Fatalf("merge-tree clean: %q %v %v", tree, conflicts, err)
	}
	if ty, _ := repo.ObjectType(ctx, vcs.RevisionID(tree)); ty != "tree" {
		t.Errorf("merge result type %q", ty)
	}
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3600))
	author := vcs.Signature{Name: "Bob", Email: "bob@example.org", When: when}
	committer := vcs.Signature{Name: "forge", Email: "forge@example.org", When: when}
	merge, err := repo.CommitTree(ctx, tree, []vcs.RevisionID{main, feat}, author, committer, "Merge change #1: feature\n\nChange: gemini://x/1\n")
	if err != nil {
		t.Fatal("commit-tree:", err)
	}
	rev, err := repo.Revision(ctx, merge)
	if err != nil || len(rev.Parents) != 2 || rev.Parents[0] != main || rev.Parents[1] != feat || rev.Tree != tree {
		t.Fatalf("merge commit: %+v %v", rev, err)
	}
	if rev.Subject != "Merge change #1: feature" || rev.Body != "Change: gemini://x/1" || rev.Author.Name != "Bob" ||
		rev.Committer.Email != "forge@example.org" || !rev.Author.When.Equal(when) {
		t.Errorf("merge commit metadata: %+v", rev)
	}
	if _, err := repo.CommitTree(ctx, "-p", nil, author, committer, "x"); err != vcs.ErrBadRef {
		t.Errorf("commit-tree injection: %v", err)
	}
	if _, err := repo.CommitTree(ctx, tree, []vcs.RevisionID{"deadbeef"}, author, committer, "x"); err == nil {
		t.Error("commit-tree with bad parent accepted")
	}

	// Conflicting change on both branches.
	work := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(work), "clone", "-q", bare, work)
	_ = os.WriteFile(filepath.Join(work, "README"), []byte("main side\n"), 0o644)
	gitIn(t, work, "commit", "-q", "-am", "main readme")
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	gitIn(t, work, "checkout", "-q", "feature")
	_ = os.WriteFile(filepath.Join(work, "README"), []byte("feature side\n"), 0o644)
	gitIn(t, work, "commit", "-q", "-am", "feature readme")
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	main2, _ := repo.Resolve(ctx, "main")
	feat2, _ := repo.Resolve(ctx, "feature")
	tree2, conflicts, err := repo.MergeTree(ctx, main2, feat2)
	if err != nil || len(conflicts) != 1 || conflicts[0] != "README" || len(tree2) < 40 {
		t.Errorf("merge-tree conflict: %q %v %v", tree2, conflicts, err)
	}
	if _, _, err := repo.MergeTree(ctx, main2, "nope"); err != vcs.ErrNotFound {
		t.Errorf("merge-tree missing: %v", err)
	}

	// update-ref transaction: create, CAS success, CAS failure, delete.
	upd := []vcs.RefUpdate{
		{Ref: "refs/changes/12/v1", New: feat},
		{Ref: "refs/changes/12/head", New: feat},
	}
	if err := repo.UpdateRefs(ctx, upd, "forge: change 12 v1"); err != nil {
		t.Fatal("create:", err)
	}
	if err := repo.UpdateRefs(ctx, upd[:1], ""); err != vcs.ErrConflict {
		t.Errorf("re-create should conflict: %v", err)
	}
	upd = []vcs.RefUpdate{
		{Ref: "refs/changes/12/v2", New: feat2},
		{Ref: "refs/changes/12/head", New: feat2, Old: feat},
	}
	if err := repo.UpdateRefs(ctx, upd, "forge: change 12 v2"); err != nil {
		t.Fatal("cas update:", err)
	}
	stale := []vcs.RefUpdate{
		{Ref: "refs/changes/12/v3", New: main2},
		{Ref: "refs/changes/12/head", New: main2, Old: feat}, // stale Old
	}
	if err := repo.UpdateRefs(ctx, stale, ""); err != vcs.ErrConflict {
		t.Errorf("stale cas: %v", err)
	}
	if _, err := repo.Resolve(ctx, "refs/changes/12/v3"); err != vcs.ErrNotFound {
		t.Error("transaction was not atomic: v3 exists")
	}
	refs, err := repo.RefsMatching(ctx, "refs/changes/12/")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]vcs.RevisionID{}
	for _, r := range refs {
		got[r.Name] = r.Target
		if r.Kind != vcs.RefOther || r.When.IsZero() {
			t.Errorf("ref %+v", r)
		}
	}
	if len(got) != 3 || got["refs/changes/12/head"] != feat2 || got["refs/changes/12/v1"] != feat || got["refs/changes/12/v2"] != feat2 {
		t.Errorf("refs matching: %v", got)
	}
	if refs, _ := repo.RefsMatching(ctx, "refs/changes/1"); len(refs) != 0 {
		t.Errorf("prefix must match whole path components: %+v", refs)
	}
	if refs, _ := repo.RefsMatching(ctx, "refs/tags/"); len(refs) != 2 || refs[0].Kind != vcs.RefTag {
		t.Errorf("tags: %+v", refs)
	}
	if _, err := repo.RefsMatching(ctx, "--all"); err != vcs.ErrBadRef {
		t.Errorf("refs injection: %v", err)
	}
	if _, err := repo.RefsMatching(ctx, "heads"); err != vcs.ErrBadRef {
		t.Errorf("refs without refs/: %v", err)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "refs/changes/12/head", Old: main2}}, ""); err != vcs.ErrConflict {
		t.Errorf("delete with stale old: %v", err)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{
		{Ref: "refs/changes/12/head", Old: feat2},
		{Ref: "refs/changes/12/v1"},
		{Ref: "refs/changes/12/v2"},
	}, "cleanup"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if refs, _ := repo.RefsMatching(ctx, "refs/changes/"); len(refs) != 0 {
		t.Errorf("refs after delete: %+v", refs)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "HEAD", New: main}}, ""); err != vcs.ErrBadRef {
		t.Errorf("non-refs/ name: %v", err)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "refs/heads/x", New: "--bad"}}, ""); err != vcs.ErrBadRef {
		t.Errorf("bad id: %v", err)
	}
	if err := repo.UpdateRefs(ctx, nil, ""); err != nil {
		t.Errorf("empty batch: %v", err)
	}
	// Fast-forward a real branch with CAS.
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "refs/heads/main", New: merge, Old: main}}, "merge"); err != vcs.ErrConflict {
		t.Errorf("main moved since merge; want conflict: %v", err)
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: "refs/heads/main", New: merge, Old: main2}}, "merge"); err != nil {
		t.Errorf("cas move: %v", err)
	}
	if got, _ := repo.Resolve(ctx, "main"); got != merge {
		t.Errorf("main after move: %v", got)
	}
}

func TestEnvProcReceiveConfig(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "r.git")
	if err := b.Init(ctx, dir, "main"); err != nil {
		t.Fatal(err)
	}
	get := func(key string) []string {
		cmd := b.Command(ctx, dir, "config", "--get-all", key)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("config %s: %v", key, err)
		}
		return strings.Fields(string(out))
	}
	if got := get("receive.procReceiveRefs"); len(got) != 2 || got[0] != "refs/for" || got[1] != "refs/changes" {
		t.Errorf("receive.procReceiveRefs = %v", got)
	}
	if got := get("receive.advertisePushOptions"); len(got) != 1 || got[0] != "true" {
		t.Errorf("receive.advertisePushOptions = %v", got)
	}
}

var _ vcs.Repository = (*Repo)(nil)
