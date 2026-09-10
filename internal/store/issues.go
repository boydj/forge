package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Issue is a tracker item.
type Issue struct {
	ID        int64
	RepoID    int64
	Number    int64
	AuthorID  int64
	Author    string
	Title     string
	Body      string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  time.Time
	Comments  int
}

// Comment is a reply on an issue or change.
type Comment struct {
	ID         int64
	RepoID     int64
	TargetKind string
	TargetID   int64
	AuthorID   int64
	Author     string
	Body       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  time.Time
}

const issueCols = `i.id, i.repo_id, i.number, i.author_id, u.name, i.title, i.body, i.state, i.created_at, i.updated_at, i.closed_at,
	(SELECT count(*) FROM comments c WHERE c.target_kind = 'issue' AND c.target_id = i.id AND c.deleted_at IS NULL)`
const issueFrom = ` FROM issues i JOIN users u ON u.id = i.author_id `

func scanIssue(row interface{ Scan(...any) error }) (*Issue, error) {
	var is Issue
	var c, up string
	var cl sql.NullString
	if err := row.Scan(&is.ID, &is.RepoID, &is.Number, &is.AuthorID, &is.Author, &is.Title, &is.Body, &is.State, &c, &up, &cl, &is.Comments); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	is.CreatedAt, is.UpdatedAt, is.ClosedAt = ParseTime(c), ParseTime(up), nullTime(cl)
	return &is, nil
}

// CreateIssue inserts an issue with the next number for the repository.
func (s *Store) CreateIssue(ctx context.Context, repoID, authorID int64, title, body string) (*Issue, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(number), 0) + 1 FROM issues WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
			return err
		}
		now := Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO issues (repo_id, number, author_id, title, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repoID, n, authorID, title, body, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `UPDATE repositories SET updated_at = ? WHERE id = ?`, now, repoID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.IssueByID(ctx, id)
}

// IssueByID loads an issue.
func (s *Store) IssueByID(ctx context.Context, id int64) (*Issue, error) {
	return scanIssue(s.db.QueryRowContext(ctx, `SELECT `+issueCols+issueFrom+`WHERE i.id = ?`, id))
}

// IssueByNumber loads an issue by repository and number.
func (s *Store) IssueByNumber(ctx context.Context, repoID, number int64) (*Issue, error) {
	return scanIssue(s.db.QueryRowContext(ctx, `SELECT `+issueCols+issueFrom+`WHERE i.repo_id = ? AND i.number = ?`, repoID, number))
}

// ListIssues lists issues of a repository by state ("" for all), newest first.
func (s *Store) ListIssues(ctx context.Context, repoID int64, state string, limit, offset int) ([]*Issue, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + issueCols + issueFrom + `WHERE i.repo_id = ?`
	args := []any{repoID}
	if state != "" {
		q += ` AND i.state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY i.updated_at DESC, i.number DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Issue
	for rows.Next() {
		is, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, is)
	}
	return out, rows.Err()
}

// CountIssues returns open and closed counts.
func (s *Store) CountIssues(ctx context.Context, repoID int64) (open, closed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(state = 'open'), 0), coalesce(sum(state = 'closed'), 0) FROM issues WHERE repo_id = ?`, repoID).Scan(&open, &closed)
	return
}

// UpdateIssue changes title and body.
func (s *Store) UpdateIssue(ctx context.Context, id int64, title, body string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE issues SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, Now(), id)
	return err
}

// SetIssueState opens or closes an issue.
func (s *Store) SetIssueState(ctx context.Context, id int64, state string) error {
	now := Now()
	var closed any
	if state == "closed" {
		closed = now
	}
	_, err := s.db.ExecContext(ctx, `UPDATE issues SET state = ?, closed_at = ?, updated_at = ? WHERE id = ?`, state, closed, now, id)
	return err
}

// touchTarget bumps the parent's updated_at when a comment lands.
func (s *Store) touchTarget(ctx context.Context, tx *sql.Tx, kind string, id int64) error {
	table := "issues"
	if kind == "change" {
		table = "changes"
	}
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// AddComment appends a comment to an issue or change.
func (s *Store) AddComment(ctx context.Context, repoID int64, kind string, targetID, authorID int64, body string) (*Comment, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		now := Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO comments (repo_id, target_kind, target_id, author_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repoID, kind, targetID, authorID, body, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return s.touchTarget(ctx, tx, kind, targetID)
	})
	if err != nil {
		return nil, err
	}
	return s.CommentByID(ctx, id)
}

const commentCols = `c.id, c.repo_id, c.target_kind, c.target_id, c.author_id, u.name, c.body, c.created_at, c.updated_at, c.deleted_at`

func scanComment(row interface{ Scan(...any) error }) (*Comment, error) {
	var c Comment
	var cr, up string
	var del sql.NullString
	if err := row.Scan(&c.ID, &c.RepoID, &c.TargetKind, &c.TargetID, &c.AuthorID, &c.Author, &c.Body, &cr, &up, &del); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt, c.DeletedAt = ParseTime(cr), ParseTime(up), nullTime(del)
	return &c, nil
}

// CommentByID loads a comment.
func (s *Store) CommentByID(ctx context.Context, id int64) (*Comment, error) {
	return scanComment(s.db.QueryRowContext(ctx, `SELECT `+commentCols+` FROM comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?`, id))
}

// ListComments lists non-deleted comments of a target, oldest first.
func (s *Store) ListComments(ctx context.Context, kind string, targetID int64) ([]*Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+commentCols+` FROM comments c JOIN users u ON u.id = c.author_id WHERE c.target_kind = ? AND c.target_id = ? AND c.deleted_at IS NULL ORDER BY c.id`, kind, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateComment edits a comment body.
func (s *Store) UpdateComment(ctx context.Context, id int64, body string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE comments SET body = ?, updated_at = ? WHERE id = ?`, body, Now(), id)
	return err
}

// DeleteComment soft-deletes a comment.
func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE comments SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, Now(), id)
	return err
}
