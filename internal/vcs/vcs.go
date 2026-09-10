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
)

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
