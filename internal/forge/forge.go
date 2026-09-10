// Package forge holds the domain services: identity, repositories,
// permissions, quotas and events. Protocol handlers (Gemini, Titan, SSH)
// call into this package; it never renders output.
package forge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

// Forge is the application service layer.
type Forge struct {
	Config *config.Config
	Store  *store.Store
	Git    *gitvcs.Backend
	Log    *slog.Logger
	// OnPush, when set, is called after a successful push (replication).
	OnPush func(r *store.Repo)
}

// Errors.
var (
	ErrNotFound      = errors.New("forge: not found")
	ErrForbidden     = errors.New("forge: forbidden")
	ErrInvalidName   = errors.New("forge: invalid name")
	ErrReservedName  = errors.New("forge: reserved name")
	ErrExists        = errors.New("forge: already exists")
	ErrQuota         = errors.New("forge: quota exceeded")
	ErrDiskFull      = errors.New("forge: insufficient disk space")
	ErrArchived      = errors.New("forge: repository is archived")
	ErrNotLeader     = errors.New("forge: this node is not the leader")
	ErrTooLarge      = errors.New("forge: too large")
	ErrUnsupported   = errors.New("forge: unsupported")
	ErrDisabled      = errors.New("forge: account disabled")
	ErrCertRevoked   = errors.New("forge: certificate revoked")
	ErrCertExpired   = errors.New("forge: certificate expired")
	ErrCertUnknown   = errors.New("forge: certificate not registered")
	ErrAuthRequired  = errors.New("forge: authentication required")
	ErrNotAcceptable = errors.New("forge: content not acceptable")
)

var (
	userNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	repoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// reserved path segments that may not be repository or user names
	reservedRepoNames = map[string]bool{"feed": true, "new": true, "edit": true, "atom.xml": true, "keys": true, "certs": true}
	reservedUserNames = map[string]bool{"feed": true, "account": true, "status": true, "admin": true, "new": true, "git": true, "root": false, "atom.xml": true, "docs": true, "help": true, "search": true, "users": true, "repos": true}
)

// ValidUserName reports whether name is acceptable for an account.
func ValidUserName(name string) error {
	if !userNameRe.MatchString(name) {
		return ErrInvalidName
	}
	if reservedUserNames[name] {
		return ErrReservedName
	}
	return nil
}

// ValidRepoName reports whether name is acceptable for a repository.
func ValidRepoName(name string) error {
	if !repoNameRe.MatchString(name) || strings.HasSuffix(name, ".git") || strings.Contains(name, "..") {
		return ErrInvalidName
	}
	if reservedRepoNames[name] {
		return ErrReservedName
	}
	return nil
}

// New wires the service layer.
func New(cfg *config.Config, st *store.Store, git *gitvcs.Backend, log *slog.Logger) *Forge {
	if log == nil {
		log = slog.Default()
	}
	return &Forge{Config: cfg, Store: st, Git: git, Log: log}
}

// RepoPath is the on-disk location of a repository.
func (f *Forge) RepoPath(owner, name string) string {
	return filepath.Join(f.Config.ReposDir(), owner, name+".git")
}

// Open returns the VCS view of a repository record.
func (f *Forge) Open(r *store.Repo) (vcs.Repository, error) {
	if r.VCS != "git" {
		return nil, ErrUnsupported
	}
	repo, err := f.Git.Open(f.RepoPath(r.Owner, r.Name))
	if err != nil {
		return nil, fmt.Errorf("%w: repository files missing for %s/%s", ErrNotFound, r.Owner, r.Name)
	}
	return repo, nil
}

// IsLeader reports whether this node leads the repository.
func (f *Forge) IsLeader(r *store.Repo) bool {
	return !f.Config.Cluster.Enabled || r.LeaderNode == f.Config.Node
}

// FreeDisk returns free bytes on the data filesystem.
func (f *Forge) FreeDisk() (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(f.Config.DataDir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// CheckDisk fails when free space is below the configured minimum plus need.
func (f *Forge) CheckDisk(need int64) error {
	free, err := f.FreeDisk()
	if err != nil {
		return err
	}
	if free-need < f.Config.Limits.MinFreeBytes {
		return ErrDiskFull
	}
	return nil
}

// EnsureDirs creates the data layout.
func (f *Forge) EnsureDirs() error {
	for _, d := range []string{f.Config.DataDir, f.Config.ReposDir(), f.Config.TmpDir(), f.Config.AssetsDir(),
		filepath.Dir(f.Config.Gemini.CertFile), filepath.Dir(f.Config.SSH.HostKeyFile), filepath.Join(f.Config.DataDir, "hooks")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	return nil
}

// Event appends an activity event.
func (f *Forge) Event(ctx context.Context, kind string, repo *store.Repo, user *store.User, subject, path string, payload []byte) {
	e := &store.Event{Kind: kind, Subject: subject, Path: path, Payload: payload}
	if repo != nil {
		e.RepoID = repo.ID
	}
	if user != nil {
		e.UserID = user.ID
	}
	if _, err := f.Store.AddEvent(ctx, e); err != nil {
		f.Log.Error("event append failed", "kind", kind, "err", err)
	}
}
