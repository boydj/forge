// Package vcs defines the version-control abstraction the forge is built on.
// Git is the first implementation (internal/vcs/git). The interface is
// deliberately close to what a forge needs to render and replicate, not to
// the full feature set of any one VCS. See docs/vcs-interface.md.
package vcs

import (
	"context"
	"errors"
	"io"
	"time"
)

// RevisionID identifies an immutable revision (a Git commit hash).
type RevisionID string

// RefKind classifies a ref.
type RefKind int

// Ref kinds.
const (
	RefBranch RefKind = iota
	RefTag
	RefOther
)

// Ref is a named pointer to a revision.
type Ref struct {
	// Name is the short name ("main", "v1.0").
	Name string
	Kind RefKind
	// Target is the revision the ref points at (peeled for annotated tags).
	Target RevisionID
	// Object is the object the ref points at before peeling (tag object for
	// annotated tags, otherwise equal to Target).
	Object RevisionID
	// When is the creator date of the target when known.
	When time.Time
	// Message is the annotated tag message, if any.
	Message string
}

// Signature is an author or committer stamp.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Revision is a commit-like object.
type Revision struct {
	ID        RevisionID
	Parents   []RevisionID
	Author    Signature
	Committer Signature
	// Subject is the first line of the message; Body the remainder.
	Subject string
	Body    string
	Tree    string
}

// EntryKind classifies a tree entry.
type EntryKind int

// Entry kinds.
const (
	EntryFile EntryKind = iota
	EntryExecutable
	EntryDir
	EntrySymlink
	EntrySubmodule
)

// TreeEntry is one entry of a directory listing.
type TreeEntry struct {
	Name string
	Kind EntryKind
	Mode string
	ID   string
	// Size is the blob size, or -1 for non-blobs.
	Size int64
}

// Blob is file content. Callers must Close it.
type Blob struct {
	ID   string
	Size int64
	// Reader yields the content; it is bounded to Size bytes.
	Reader io.ReadCloser
}

// Close releases the underlying subprocess.
func (b *Blob) Close() error {
	if b == nil || b.Reader == nil {
		return nil
	}
	return b.Reader.Close()
}

// DiffStat summarises one file in a diff.
type DiffStat struct {
	Path    string
	OldPath string
	Added   int
	Deleted int
	Binary  bool
}

// Diff is a rendered unified diff plus per-file statistics.
type Diff struct {
	Stats []DiffStat
	// Patch is unified diff text, possibly truncated.
	Patch     string
	Truncated bool
}

// LogOptions filters a history query.
type LogOptions struct {
	// Path restricts history to revisions touching Path (empty: all).
	Path  string
	Skip  int
	Limit int
}

// Errors returned by implementations.
var (
	ErrNotFound   = errors.New("vcs: not found")
	ErrEmptyRepo  = errors.New("vcs: repository has no revisions")
	ErrBadRef     = errors.New("vcs: invalid ref name")
	ErrBadPath    = errors.New("vcs: invalid path")
	ErrTooLarge   = errors.New("vcs: object too large")
	ErrTimeout    = errors.New("vcs: operation timed out")
	ErrIsDir      = errors.New("vcs: path is a directory")
	ErrNotDir     = errors.New("vcs: path is not a directory")
	ErrUnexpected = errors.New("vcs: unexpected tool output")
	// ErrConflict is returned by UpdateRefs when a compare-and-swap fails
	// (the ref moved, already exists, or is locked by another writer).
	ErrConflict = errors.New("vcs: ref update conflict")
)

// RefUpdate is one entry of an atomic ref transaction (see
// Repository.UpdateRefs). Ref is a full ref name ("refs/heads/main").
type RefUpdate struct {
	Ref string
	// New is the revision to point Ref at; "" deletes the ref.
	New RevisionID
	// Old is the expected current value: "" means the ref must not exist
	// (for a delete: unconditional); otherwise the update is a
	// compare-and-swap against Old.
	Old RevisionID
}

// Repository is a read view of one repository.
type Repository interface {
	// Path is the on-disk location.
	Path() string
	// Empty reports whether the repository has no revisions.
	Empty(ctx context.Context) (bool, error)
	// DefaultBranch returns the branch HEAD points to.
	DefaultBranch(ctx context.Context) (string, error)
	// Refs lists branches and tags.
	Refs(ctx context.Context) ([]Ref, error)
	// Resolve turns a ref name or revision id into a revision id.
	Resolve(ctx context.Context, ref string) (RevisionID, error)
	// Revision loads one revision.
	Revision(ctx context.Context, id RevisionID) (*Revision, error)
	// Log lists revisions reachable from id, newest first.
	Log(ctx context.Context, id RevisionID, opts LogOptions) ([]*Revision, error)
	// Tree lists a directory at a revision. Path "" is the root.
	Tree(ctx context.Context, id RevisionID, path string) ([]TreeEntry, error)
	// Blob opens a file at a revision.
	Blob(ctx context.Context, id RevisionID, path string) (*Blob, error)
	// Diff renders the change introduced by id against its first parent.
	Diff(ctx context.Context, id RevisionID, maxBytes int64) (*Diff, error)
	// DiffRange renders the change between two revisions (base..head).
	DiffRange(ctx context.Context, base, head RevisionID, maxBytes int64) (*Diff, error)
	// Size reports the on-disk size in bytes.
	Size(ctx context.Context) (int64, error)
	// Check verifies integrity; it returns an error describing corruption.
	Check(ctx context.Context) error

	// MergeBase returns the best common ancestor of a and b, or ErrNotFound
	// when the histories are unrelated.
	MergeBase(ctx context.Context, a, b RevisionID) (RevisionID, error)
	// IsAncestor reports whether a is an ancestor of (or equal to) b.
	IsAncestor(ctx context.Context, a, b RevisionID) (bool, error)
	// CountCommits counts the revisions in base..head.
	CountCommits(ctx context.Context, base, head RevisionID) (int, error)
	// ListCommits lists base..head oldest first, at most limit (<=0: 500).
	// When the range holds more than limit revisions the newest limit are
	// returned (git applies -n before --reverse); check CountCommits first.
	ListCommits(ctx context.Context, base, head RevisionID, limit int) ([]*Revision, error)
	// RangeDiff renders `git range-diff base1..head1 base2..head2` without
	// colour, bounded to maxBytes (<=0: 1 MiB); truncated reports whether
	// output was cut.
	RangeDiff(ctx context.Context, base1, head1, base2, head2 RevisionID, maxBytes int64) (text string, truncated bool, err error)
	// FormatPatch streams `git format-patch --stdout base..head` (an mbox)
	// to w, stopping after maxBytes (<=0: 64 MiB) and returning ErrTooLarge
	// when the output was cut.
	FormatPatch(ctx context.Context, base, head RevisionID, maxBytes int64, w io.Writer) error
	// DiffPath is DiffRange restricted to one path ("" means no filter).
	DiffPath(ctx context.Context, base, head RevisionID, path string, maxBytes int64) (*Diff, error)
	// MergeTree merges theirs into ours without a working tree
	// (`git merge-tree --write-tree`, git >= 2.38) and returns the resulting
	// tree id. When the merge is conflicted err is nil and conflicts lists
	// the conflicted paths; the tree then contains conflict markers.
	MergeTree(ctx context.Context, ours, theirs RevisionID) (tree string, conflicts []string, err error)
	// CommitTree creates a commit object for tree with the given parents,
	// identities and message and returns its id.
	CommitTree(ctx context.Context, tree string, parents []RevisionID, author, committer Signature, message string) (RevisionID, error)
	// UpdateRefs applies all updates atomically (all or nothing). It returns
	// ErrConflict when any compare-and-swap fails. reason is recorded in the
	// reflog.
	UpdateRefs(ctx context.Context, updates []RefUpdate, reason string) error
	// RefsMatching lists refs whose full name is prefix or lies under it
	// (e.g. "refs/changes/12/"). Name is the full ref name.
	RefsMatching(ctx context.Context, prefix string) ([]Ref, error)
	// ObjectType returns "commit", "tag", "tree" or "blob", or ErrNotFound.
	ObjectType(ctx context.Context, id RevisionID) (string, error)
}

// Backend creates and opens repositories.
type Backend interface {
	// Name is the VCS name ("git").
	Name() string
	// Init creates an empty repository at path with the given default branch.
	Init(ctx context.Context, path, defaultBranch string) error
	// Open opens an existing repository.
	Open(path string) (Repository, error)
	// SetDefaultBranch changes the branch HEAD points to.
	SetDefaultBranch(ctx context.Context, path, branch string) error
	// Fetch mirrors refs and objects from a remote URL into path
	// (replication). Refs are force-updated to match the source.
	Fetch(ctx context.Context, path, remoteURL string) error
}
