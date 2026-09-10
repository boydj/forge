package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"as215520.net/forge/internal/store"
)

// Access describes what an identity may do with a repository.
type Access struct {
	Repo *store.Repo
	Role store.Role
}

// CanRead reports read permission.
func (a Access) CanRead() bool { return a.Role != store.RoleNone }

// CanWrite reports push/issue-management permission.
func (a Access) CanWrite() bool { return a.Role == store.RoleWrite || a.Role == store.RoleAdmin }

// CanAdmin reports settings/delete permission.
func (a Access) CanAdmin() bool { return a.Role == store.RoleAdmin }

// Permission computes the role of a viewer on a repository.
func (f *Forge) Permission(ctx context.Context, viewer *store.User, r *store.Repo) (store.Role, error) {
	if viewer != nil {
		if viewer.Admin || viewer.ID == r.OwnerID {
			return store.RoleAdmin, nil
		}
		role, err := f.Store.CollaboratorRole(ctx, r.ID, viewer.ID)
		if err != nil {
			return store.RoleNone, err
		}
		if role != store.RoleNone {
			return role, nil
		}
	}
	if !r.Private {
		return store.RoleRead, nil
	}
	return store.RoleNone, nil
}

// LookupRepo resolves owner/name for a viewer. Repositories the viewer
// cannot read are reported as not found so private names do not leak.
func (f *Forge) LookupRepo(ctx context.Context, viewer *store.User, owner, name string) (Access, error) {
	r, err := f.Store.RepoByPath(ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return Access{}, ErrNotFound
	}
	if err != nil {
		return Access{}, err
	}
	role, err := f.Permission(ctx, viewer, r)
	if err != nil {
		return Access{}, err
	}
	if role == store.RoleNone {
		return Access{}, ErrNotFound
	}
	return Access{Repo: r, Role: role}, nil
}

// CreateRepoOptions are the inputs to CreateRepo.
type CreateRepoOptions struct {
	Name          string
	Description   string
	Private       bool
	DefaultBranch string
}

// CreateRepo creates a repository owned by u.
func (f *Forge) CreateRepo(ctx context.Context, u *store.User, o CreateRepoOptions) (*store.Repo, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	name := strings.ToLower(strings.TrimSpace(o.Name))
	if err := ValidRepoName(name); err != nil {
		return nil, err
	}
	if o.DefaultBranch == "" {
		o.DefaultBranch = "main"
	}
	if len(o.Description) > 512 {
		return nil, ErrTooLarge
	}
	n, size, err := f.Store.CountReposByOwner(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if !u.Admin && (n >= f.Config.Limits.MaxReposPerUser || size >= f.Config.Limits.MaxUserBytes) {
		return nil, ErrQuota
	}
	if err := f.CheckDisk(0); err != nil {
		return nil, err
	}
	path := f.RepoPath(u.Name, name)
	if _, err := os.Stat(path); err == nil {
		return nil, ErrExists
	}
	r, err := f.Store.CreateRepo(ctx, &store.Repo{
		OwnerID: u.ID, Name: name, Description: strings.TrimSpace(o.Description), Private: o.Private,
		DefaultBranch: o.DefaultBranch, VCS: "git", LeaderNode: f.Config.Node,
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	if err := f.Git.Init(ctx, path, o.DefaultBranch); err != nil {
		_ = f.Store.PurgeRepo(ctx, r.ID)
		return nil, err
	}
	f.Event(ctx, store.EventRepoCreate, r, u, fmt.Sprintf("%s created %s/%s", u.Name, u.Name, name), "/~"+u.Name+"/"+name+"/", nil)
	return r, nil
}

// UpdateRepoOptions are editable metadata fields; nil pointers are unchanged.
type UpdateRepoOptions struct {
	Description   *string
	Private       *bool
	Archived      *bool
	DefaultBranch *string
}

// UpdateRepo changes metadata.
func (f *Forge) UpdateRepo(ctx context.Context, u *store.User, r *store.Repo, o UpdateRepoOptions) error {
	role, err := f.Permission(ctx, u, r)
	if err != nil {
		return err
	}
	if role != store.RoleAdmin {
		return ErrForbidden
	}
	if o.Description != nil {
		if len(*o.Description) > 512 {
			return ErrTooLarge
		}
		r.Description = strings.TrimSpace(*o.Description)
	}
	if o.Private != nil {
		r.Private = *o.Private
	}
	if o.Archived != nil {
		r.Archived = *o.Archived
	}
	if o.DefaultBranch != nil && *o.DefaultBranch != r.DefaultBranch {
		if err := f.Git.SetDefaultBranch(ctx, f.RepoPath(r.Owner, r.Name), *o.DefaultBranch); err != nil {
			return ErrInvalidName
		}
		r.DefaultBranch = *o.DefaultBranch
	}
	if err := f.Store.UpdateRepo(ctx, r); err != nil {
		return err
	}
	kind := store.EventRepoUpdate
	if o.Archived != nil && *o.Archived {
		kind = store.EventRepoArchive
	}
	f.Event(ctx, kind, r, u, fmt.Sprintf("%s updated %s/%s", u.Name, r.Owner, r.Name), "/~"+r.Owner+"/"+r.Name+"/", nil)
	return nil
}

// DeleteRetention is how long a deleted repository can be restored.
const DeleteRetention = 7 * 24 * time.Hour

// DeleteRepo soft-deletes a repository. The confirm string must equal the
// repository's full name to guard against accidents.
func (f *Forge) DeleteRepo(ctx context.Context, u *store.User, r *store.Repo, confirm string) error {
	role, err := f.Permission(ctx, u, r)
	if err != nil {
		return err
	}
	if role != store.RoleAdmin {
		return ErrForbidden
	}
	if confirm != r.Owner+"/"+r.Name {
		return ErrNotAcceptable
	}
	if err := f.Store.SoftDeleteRepo(ctx, r.ID); err != nil {
		return err
	}
	f.Event(ctx, store.EventRepoDelete, nil, u, fmt.Sprintf("%s deleted %s/%s", u.Name, r.Owner, r.Name), "/~"+u.Name+"/", nil)
	return nil
}

// PurgeDeleted removes repositories deleted longer than DeleteRetention ago.
func (f *Forge) PurgeDeleted(ctx context.Context) (int, error) {
	repos, err := f.Store.ListDeletedRepos(ctx, time.Now().Add(-DeleteRetention))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range repos {
		path := f.RepoPath(r.Owner, r.Name)
		trash := filepath.Join(f.Config.TmpDir(), fmt.Sprintf("purge-%d-%s.git", r.ID, r.Name))
		if err := os.Rename(path, trash); err != nil && !os.IsNotExist(err) {
			f.Log.Error("purge rename failed", "repo", path, "err", err)
			continue
		}
		if err := os.RemoveAll(trash); err != nil {
			f.Log.Error("purge remove failed", "repo", trash, "err", err)
		}
		if err := f.Store.PurgeRepo(ctx, r.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RefreshSize recomputes and stores the on-disk size.
func (f *Forge) RefreshSize(ctx context.Context, r *store.Repo) (int64, error) {
	repo, err := f.Open(r)
	if err != nil {
		return 0, err
	}
	size, err := repo.Size(ctx)
	if err != nil {
		return 0, err
	}
	r.SizeBytes = size
	return size, f.Store.SetRepoSize(ctx, r.ID, size)
}

// SetCollaborator grants or removes a role for a named user.
func (f *Forge) SetCollaborator(ctx context.Context, u *store.User, r *store.Repo, name string, role store.Role) error {
	perm, err := f.Permission(ctx, u, r)
	if err != nil {
		return err
	}
	if perm != store.RoleAdmin {
		return ErrForbidden
	}
	target, err := f.Store.UserByName(ctx, strings.ToLower(strings.TrimSpace(name)))
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if target.ID == r.OwnerID {
		return ErrNotAcceptable
	}
	if err := f.Store.SetCollaborator(ctx, r.ID, target.ID, role); err != nil {
		return err
	}
	f.Event(ctx, store.EventRepoUpdate, r, u, fmt.Sprintf("%s changed collaborators of %s/%s", u.Name, r.Owner, r.Name), "/~"+r.Owner+"/"+r.Name+"/", nil)
	return nil
}
