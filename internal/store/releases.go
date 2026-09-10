package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Release is a tagged release with notes and assets.
type Release struct {
	ID        int64
	RepoID    int64
	Tag       string
	Title     string
	Body      string
	AuthorID  int64
	Author    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Assets    []*ReleaseAsset
}

// ReleaseAsset is a file attached to a release.
type ReleaseAsset struct {
	ID        int64
	ReleaseID int64
	Name      string
	Size      int64
	MIME      string
	SHA256    string
	CreatedAt time.Time
}

const releaseCols = `r.id, r.repo_id, r.tag, r.title, r.body, r.author_id, u.name, r.created_at, r.updated_at`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	var c, up string
	if err := row.Scan(&r.ID, &r.RepoID, &r.Tag, &r.Title, &r.Body, &r.AuthorID, &r.Author, &c, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt, r.UpdatedAt = ParseTime(c), ParseTime(up)
	return &r, nil
}

// CreateRelease inserts a release.
func (s *Store) CreateRelease(ctx context.Context, repoID, authorID int64, tag, title, body string) (*Release, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO releases (repo_id, tag, title, body, author_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		repoID, tag, title, body, authorID, now, now)
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `UPDATE repositories SET updated_at = ? WHERE id = ?`, now, repoID)
	return s.ReleaseByID(ctx, id)
}

// ReleaseByID loads a release with assets.
func (s *Store) ReleaseByID(ctx context.Context, id int64) (*Release, error) {
	r, err := scanRelease(s.db.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases r JOIN users u ON u.id = r.author_id WHERE r.id = ?`, id))
	if err != nil {
		return nil, err
	}
	r.Assets, err = s.ListReleaseAssets(ctx, r.ID)
	return r, err
}

// ReleaseByTag loads a release by repository and tag.
func (s *Store) ReleaseByTag(ctx context.Context, repoID int64, tag string) (*Release, error) {
	r, err := scanRelease(s.db.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases r JOIN users u ON u.id = r.author_id WHERE r.repo_id = ? AND r.tag = ?`, repoID, tag))
	if err != nil {
		return nil, err
	}
	r.Assets, err = s.ListReleaseAssets(ctx, r.ID)
	return r, err
}

// ListReleases lists releases newest first (without assets).
func (s *Store) ListReleases(ctx context.Context, repoID int64, limit int) ([]*Release, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseCols+` FROM releases r JOIN users u ON u.id = r.author_id WHERE r.repo_id = ? ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRelease edits title and body.
func (s *Store) UpdateRelease(ctx context.Context, id int64, title, body string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, Now(), id)
	return err
}

// DeleteRelease removes a release and its asset rows.
func (s *Store) DeleteRelease(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE id = ?`, id)
	return err
}

// AddReleaseAsset records an asset (replacing a same-named one).
func (s *Store) AddReleaseAsset(ctx context.Context, a *ReleaseAsset) (*ReleaseAsset, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO release_assets (release_id, name, size, mime, sha256, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(release_id, name) DO UPDATE SET size = excluded.size, mime = excluded.mime, sha256 = excluded.sha256, created_at = excluded.created_at`,
		a.ReleaseID, a.Name, a.Size, a.MIME, a.SHA256, Now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return s.ReleaseAsset(ctx, a.ReleaseID, a.Name)
	}
	return s.ReleaseAsset(ctx, a.ReleaseID, a.Name)
}

// ReleaseAsset loads one asset by name.
func (s *Store) ReleaseAsset(ctx context.Context, releaseID int64, name string) (*ReleaseAsset, error) {
	var a ReleaseAsset
	var c string
	err := s.db.QueryRowContext(ctx, `SELECT id, release_id, name, size, mime, sha256, created_at FROM release_assets WHERE release_id = ? AND name = ?`, releaseID, name).
		Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Size, &a.MIME, &a.SHA256, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt = ParseTime(c)
	return &a, nil
}

// ListReleaseAssets lists assets of a release.
func (s *Store) ListReleaseAssets(ctx context.Context, releaseID int64) ([]*ReleaseAsset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, release_id, name, size, mime, sha256, created_at FROM release_assets WHERE release_id = ? ORDER BY name`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReleaseAsset
	for rows.Next() {
		var a ReleaseAsset
		var c string
		if err := rows.Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Size, &a.MIME, &a.SHA256, &c); err != nil {
			return nil, err
		}
		a.CreatedAt = ParseTime(c)
		out = append(out, &a)
	}
	return out, rows.Err()
}

// DeleteReleaseAsset removes an asset row.
func (s *Store) DeleteReleaseAsset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM release_assets WHERE id = ?`, id)
	return err
}

// AssetBytesByOwner sums release asset sizes for quota accounting.
func (s *Store) AssetBytesByOwner(ctx context.Context, ownerID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(a.size), 0) FROM release_assets a JOIN releases r ON r.id = a.release_id JOIN repositories p ON p.id = r.repo_id WHERE p.owner_id = ?`, ownerID).Scan(&n)
	return n, err
}
