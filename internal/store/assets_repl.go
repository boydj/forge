package store

import (
	"context"
	"database/sql"
	"errors"
)

// RepoAsset is a release asset joined with its release tag: what a replica
// needs to locate, verify and fetch the file (see docs/replication.md).
type RepoAsset struct {
	ID        int64
	ReleaseID int64
	Tag       string
	Name      string
	Size      int64
	SHA256    string
}

const repoAssetCols = `a.id, a.release_id, r.tag, a.name, a.size, a.sha256 FROM release_assets a JOIN releases r ON r.id = a.release_id`

// RepoAssets lists every release asset of a repository, by tag then name.
func (s *Store) RepoAssets(ctx context.Context, repoID int64) ([]RepoAsset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoAssetCols+` WHERE r.repo_id = ? ORDER BY r.tag, a.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoAsset
	for rows.Next() {
		var a RepoAsset
		if err := rows.Scan(&a.ID, &a.ReleaseID, &a.Tag, &a.Name, &a.Size, &a.SHA256); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RepoAssetByTagName loads one release asset of a repository by release tag
// and asset name; ErrNotFound when there is no such row.
func (s *Store) RepoAssetByTagName(ctx context.Context, repoID int64, tag, name string) (*RepoAsset, error) {
	var a RepoAsset
	err := s.db.QueryRowContext(ctx, `SELECT `+repoAssetCols+` WHERE r.repo_id = ? AND r.tag = ? AND a.name = ?`, repoID, tag, name).
		Scan(&a.ID, &a.ReleaseID, &a.Tag, &a.Name, &a.Size, &a.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
