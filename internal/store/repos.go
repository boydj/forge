package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Repo is a repository record.
type Repo struct {
	ID            int64
	OwnerID       int64
	Owner         string
	Name          string
	Description   string
	Private       bool
	Archived      bool
	DefaultBranch string
	VCS           string
	LeaderNode    string
	SizeBytes     int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	PushedAt      time.Time
	DeletedAt     time.Time
}

// Role is a collaborator role.
type Role string

// Roles.
const (
	RoleNone  Role = ""
	RoleRead  Role = "read"
	RoleWrite Role = "write"
	RoleAdmin Role = "admin"
)

const repoCols = `r.id, r.owner_id, u.name, r.name, r.description, r.private, r.archived, r.default_branch, r.vcs, r.leader_node, r.size_bytes, r.created_at, r.updated_at, r.pushed_at, r.deleted_at`
const repoFrom = ` FROM repositories r JOIN users u ON u.id = r.owner_id `

func scanRepo(row interface{ Scan(...any) error }) (*Repo, error) {
	var r Repo
	var c, up string
	var pu, del sql.NullString
	if err := row.Scan(&r.ID, &r.OwnerID, &r.Owner, &r.Name, &r.Description, &r.Private, &r.Archived, &r.DefaultBranch, &r.VCS, &r.LeaderNode, &r.SizeBytes, &c, &up, &pu, &del); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt, r.PushedAt, r.DeletedAt = ParseTime(c), ParseTime(up), nullTime(pu), nullTime(del)
	return &r, nil
}

// CreateRepo inserts a repository record.
func (s *Store) CreateRepo(ctx context.Context, r *Repo) (*Repo, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO repositories (owner_id, name, description, private, default_branch, vcs, leader_node, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.OwnerID, r.Name, r.Description, r.Private, r.DefaultBranch, r.VCS, r.LeaderNode, now, now)
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.RepoByID(ctx, id)
}

// RepoByID loads a repository (including soft-deleted).
func (s *Store) RepoByID(ctx context.Context, id int64) (*Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoCols+repoFrom+`WHERE r.id = ?`, id))
}

// RepoByPath loads owner/name, excluding soft-deleted repositories.
func (s *Store) RepoByPath(ctx context.Context, owner, name string) (*Repo, error) {
	return scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoCols+repoFrom+`WHERE u.name = ? AND r.name = ? AND r.deleted_at IS NULL`, owner, name))
}

// UpdateRepo updates mutable metadata.
func (s *Store) UpdateRepo(ctx context.Context, r *Repo) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET description = ?, private = ?, archived = ?, default_branch = ?, leader_node = ?, updated_at = ? WHERE id = ?`,
		r.Description, r.Private, r.Archived, r.DefaultBranch, r.LeaderNode, Now(), r.ID)
	return err
}

// RecordPush stamps pushed_at/updated_at and size.
func (s *Store) RecordPush(ctx context.Context, id, size int64) error {
	now := Now()
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET pushed_at = ?, updated_at = ?, size_bytes = ? WHERE id = ?`, now, now, size, id)
	return err
}

// SetRepoSize updates the cached size.
func (s *Store) SetRepoSize(ctx context.Context, id, size int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET size_bytes = ? WHERE id = ?`, size, id)
	return err
}

// SoftDeleteRepo marks a repository deleted; it disappears from lookups and
// is purged by maintenance after the retention window.
func (s *Store) SoftDeleteRepo(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, Now(), Now(), id)
	return err
}

// RestoreRepo undoes a soft delete.
func (s *Store) RestoreRepo(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET deleted_at = NULL, updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// PurgeRepo removes the record permanently.
func (s *Store) PurgeRepo(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = ?`, id)
	return err
}

// ListDeletedRepos lists repositories soft-deleted before cutoff.
func (s *Store) ListDeletedRepos(ctx context.Context, cutoff time.Time) ([]*Repo, error) {
	return s.queryRepos(ctx, `SELECT `+repoCols+repoFrom+`WHERE r.deleted_at IS NOT NULL AND r.deleted_at < ? ORDER BY r.id`, cutoff.UTC().Format(time.RFC3339))
}

// RepoListOptions filter listings.
type RepoListOptions struct {
	// Viewer is the user id viewing (0: anonymous); private repos are
	// included only when visible to the viewer.
	Viewer int64
	// Owner restricts to a user id (0: all).
	Owner  int64
	Limit  int
	Offset int
}

// ListRepos lists visible, non-deleted repositories, most recently updated first.
func (s *Store) ListRepos(ctx context.Context, o RepoListOptions) ([]*Repo, error) {
	if o.Limit <= 0 {
		o.Limit = 50
	}
	q := `SELECT ` + repoCols + repoFrom + `WHERE r.deleted_at IS NULL AND (r.private = 0 OR r.owner_id = ?1 OR EXISTS (SELECT 1 FROM collaborators c WHERE c.repo_id = r.id AND c.user_id = ?1) OR EXISTS (SELECT 1 FROM users a WHERE a.id = ?1 AND a.admin = 1))`
	args := []any{o.Viewer}
	if o.Owner != 0 {
		q += ` AND r.owner_id = ?2`
		args = append(args, o.Owner)
	} else {
		args = append(args, 0)
	}
	q += ` ORDER BY r.updated_at DESC, r.id DESC LIMIT ?3 OFFSET ?4`
	args = append(args, o.Limit, o.Offset)
	return s.queryRepos(ctx, q, args...)
}

// AllRepos lists every non-deleted repository (maintenance, replication).
func (s *Store) AllRepos(ctx context.Context) ([]*Repo, error) {
	return s.queryRepos(ctx, `SELECT `+repoCols+repoFrom+`WHERE r.deleted_at IS NULL ORDER BY r.id`)
}

// CountReposByOwner counts non-deleted repositories of a user.
func (s *Store) CountReposByOwner(ctx context.Context, owner int64) (int, int64, error) {
	var n int
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0) FROM repositories WHERE owner_id = ? AND deleted_at IS NULL`, owner).Scan(&n, &size)
	return n, size, err
}

func (s *Store) queryRepos(ctx context.Context, q string, args ...any) ([]*Repo, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Collaborator is a user with a role on a repository.
type Collaborator struct {
	UserID int64
	Name   string
	Role   Role
}

// SetCollaborator adds or updates a collaborator; RoleNone removes.
func (s *Store) SetCollaborator(ctx context.Context, repoID, userID int64, role Role) error {
	if role == RoleNone {
		_, err := s.db.ExecContext(ctx, `DELETE FROM collaborators WHERE repo_id = ? AND user_id = ?`, repoID, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO collaborators (repo_id, user_id, role, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(repo_id, user_id) DO UPDATE SET role = excluded.role`, repoID, userID, string(role), Now())
	return err
}

// CollaboratorRole returns the explicit role of a user on a repo (RoleNone if none).
func (s *Store) CollaboratorRole(ctx context.Context, repoID, userID int64) (Role, error) {
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM collaborators WHERE repo_id = ? AND user_id = ?`, repoID, userID).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleNone, nil
	}
	return Role(r), err
}

// ListCollaborators lists collaborators with names.
func (s *Store) ListCollaborators(ctx context.Context, repoID int64) ([]Collaborator, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.user_id, u.name, c.role FROM collaborators c JOIN users u ON u.id = c.user_id WHERE c.repo_id = ? ORDER BY u.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collaborator
	for rows.Next() {
		var c Collaborator
		var role string
		if err := rows.Scan(&c.UserID, &c.Name, &role); err != nil {
			return nil, err
		}
		c.Role = Role(role)
		out = append(out, c)
	}
	return out, rows.Err()
}
