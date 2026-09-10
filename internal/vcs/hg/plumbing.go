package hg

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"

	"as215520.net/forge/internal/vcs"
)

// This file holds the change-review plumbing (ADR 0012 §13). Mercurial
// covers the read side with revsets; the write side (server-side merges,
// commit creation, atomic ref transactions) has no working-copy-free
// equivalent and returns ErrUnsupported. docs/mercurial.md discusses the
// options.

// exists returns ErrNotFound unless every id names a changeset.
func (r *Repo) exists(ctx context.Context, ids ...vcs.RevisionID) error {
	seen := map[vcs.RevisionID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := r.one(ctx, revsetID(id)); err != nil {
			return err
		}
	}
	return nil
}

// nodes runs `hg log -r <revset> -T '{node}\n'` and returns the ids in the
// revset's order.
func (r *Repo) nodes(ctx context.Context, revset string) ([]vcs.RevisionID, error) {
	out, err := r.run(ctx, "log", "-r", revset, "-T", "{node}\\n")
	if err != nil {
		return nil, notFoundOr(err)
	}
	var ids []vcs.RevisionID
	for _, l := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(l) == 40 {
			ids = append(ids, vcs.RevisionID(l))
		}
	}
	return ids, nil
}

// rangeSet is git's base..head: only(head, base) = ancestors(head) -
// ancestors(base).
func rangeSet(base, head vcs.RevisionID) string {
	return "only(" + revsetID(head) + ", " + revsetID(base) + ")"
}

// MergeBase implements vcs.Repository: ancestor(a, b).
func (r *Repo) MergeBase(ctx context.Context, a, b vcs.RevisionID) (vcs.RevisionID, error) {
	if err := checkIDs(a, b); err != nil {
		return "", err
	}
	return r.one(ctx, "ancestor("+revsetID(a)+", "+revsetID(b)+")")
}

// IsAncestor implements vcs.Repository: a in ancestors(b).
func (r *Repo) IsAncestor(ctx context.Context, a, b vcs.RevisionID) (bool, error) {
	if err := checkIDs(a, b); err != nil {
		return false, err
	}
	_, err := r.one(ctx, revsetID(a)+" and ancestors("+revsetID(b)+")")
	if err == nil {
		return true, nil
	}
	if err != vcs.ErrNotFound {
		return false, err
	}
	if err := r.exists(ctx, a, b); err != nil {
		return false, err
	}
	return false, nil
}

// CountCommits implements vcs.Repository.
func (r *Repo) CountCommits(ctx context.Context, base, head vcs.RevisionID) (int, error) {
	if err := checkIDs(base, head); err != nil {
		return 0, err
	}
	if err := r.exists(ctx, base, head); err != nil {
		return 0, err
	}
	ids, err := r.nodes(ctx, rangeSet(base, head))
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// ListCommits implements vcs.Repository: last(only(head, base), n), which
// Mercurial yields oldest first.
func (r *Repo) ListCommits(ctx context.Context, base, head vcs.RevisionID, limit int) ([]*vcs.Revision, error) {
	if err := checkIDs(base, head); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	if err := r.exists(ctx, base, head); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "log", "-r", "last("+rangeSet(base, head)+", "+strconv.Itoa(limit)+")", "-T", logTemplate)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return parseRevisions(out)
}

// RangeDiff implements vcs.Repository. Mercurial has no range-diff; the
// evolve extension's `hg obslog --patch` is the nearest thing and needs
// obsolescence markers, which the forge does not enable.
func (r *Repo) RangeDiff(ctx context.Context, base1, head1, base2, head2 vcs.RevisionID, maxBytes int64) (string, bool, error) {
	if err := checkIDs(base1, head1, base2, head2); err != nil {
		return "", false, err
	}
	return "", false, ErrUnsupported
}

// FormatPatch implements vcs.Repository: `hg export --git -r only(head,
// base)`. The output is a sequence of "# HG changeset patch" documents, not
// an mbox: `hg import` applies it, `git am` does not.
func (r *Repo) FormatPatch(ctx context.Context, base, head vcs.RevisionID, maxBytes int64, w io.Writer) error {
	if err := checkIDs(base, head); err != nil {
		return err
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	if err := r.exists(ctx, base, head); err != nil {
		return err
	}
	args := []string{"export", "--git", "-r", rangeSet(base, head)}
	var werr error
	truncated := false
	err := r.b.stream(ctx, r.path, args, func(rd io.Reader) (bool, error) {
		if _, werr = io.Copy(w, io.LimitReader(rd, maxBytes)); werr != nil {
			return true, werr
		}
		var probe [1]byte
		if n, _ := rd.Read(probe[:]); n > 0 {
			truncated = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return notFoundOr(err)
	}
	if truncated {
		return vcs.ErrTooLarge
	}
	return nil
}

// DiffPath implements vcs.Repository: `hg diff -r base -r head --git [--
// path:P]`.
func (r *Repo) DiffPath(ctx context.Context, base, head vcs.RevisionID, path string, maxBytes int64) (*vcs.Diff, error) {
	if err := checkIDs(base, head); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	if err := r.exists(ctx, base, head); err != nil {
		return nil, err
	}
	args := []string{"diff", "--git", "-r", revsetID(base), "-r", revsetID(head)}
	if path != "" {
		args = append(args, "--", "path:"+path)
	}
	return r.diff(ctx, maxBytes, args...)
}

// MergeTree implements vcs.Repository. Mercurial merges need a working
// copy (or an in-memory merge through the Python API); see docs/mercurial.md.
func (r *Repo) MergeTree(ctx context.Context, ours, theirs vcs.RevisionID) (string, []string, error) {
	if err := checkIDs(ours, theirs); err != nil {
		return "", nil, err
	}
	return "", nil, ErrUnsupported
}

// CommitTree implements vcs.Repository. There are no tree objects to commit.
func (r *Repo) CommitTree(ctx context.Context, tree string, parents []vcs.RevisionID, author, committer vcs.Signature, message string) (vcs.RevisionID, error) {
	return "", ErrUnsupported
}

// UpdateRefs implements vcs.Repository. Named branches are changeset
// metadata and bookmarks have no compare-and-swap transaction on the
// command line; see docs/mercurial.md for the proposed design.
func (r *Repo) UpdateRefs(ctx context.Context, updates []vcs.RefUpdate, reason string) error {
	if len(updates) == 0 {
		return nil
	}
	for _, u := range updates {
		if !strings.HasPrefix(u.Ref, "refs/") {
			return vcs.ErrBadRef
		}
	}
	return ErrUnsupported
}

// RefsMatching implements vcs.Repository for the two namespaces Mercurial
// can express: refs/heads/ (branches and bookmarks) and refs/tags/. Any
// other prefix (refs/changes/, refs/for/) is ErrUnsupported.
func (r *Repo) RefsMatching(ctx context.Context, prefix string) ([]vcs.Ref, error) {
	if prefix == "" || len(prefix) > 1024 || strings.ContainsAny(prefix, "\x00\"\\") {
		return nil, vcs.ErrBadRef
	}
	var refs []vcs.Ref
	var err error
	switch {
	case strings.HasPrefix(prefix, "refs/heads/") || prefix == "refs/heads":
		refs, err = r.refs(ctx, true, false, "refs/heads/", "")
	case strings.HasPrefix(prefix, "refs/tags/") || prefix == "refs/tags":
		refs, err = r.refs(ctx, false, true, "", "refs/tags/")
	case strings.HasPrefix(prefix, "refs/"):
		return nil, ErrUnsupported
	default:
		return nil, vcs.ErrBadRef
	}
	if err != nil {
		return nil, err
	}
	var out []vcs.Ref
	for _, ref := range refs {
		if ref.Name == prefix || strings.HasPrefix(ref.Name, strings.TrimSuffix(prefix, "/")+"/") {
			out = append(out, ref)
		}
	}
	return out, nil
}

// ObjectType implements vcs.Repository. Only changesets are addressable by
// id from the command line; manifest and filelog nodes are not exposed.
func (r *Repo) ObjectType(ctx context.Context, id vcs.RevisionID) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	if _, err := r.one(ctx, revsetID(id)); err != nil {
		return "", err
	}
	return "commit", nil
}
