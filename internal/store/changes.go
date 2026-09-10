package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Change is a proposed change (ADR 0012).
type Change struct {
	ID           int64
	RepoID       int64
	Number       int64
	AuthorID     int64
	Author       string
	Title        string
	Body         string
	Topic        string
	TargetBranch string
	State        string
	Version      int
	HeadRev      string
	BaseRev      string
	MergedRev    string
	MergedBy     int64
	ClosedBy     int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	MergedAt     time.Time
	ClosedAt     time.Time
}

// ChangeVersion is one pushed version of a change.
type ChangeVersion struct {
	ID        int64
	ChangeID  int64
	Number    int
	HeadRev   string
	BaseRev   string
	Commits   int
	PushedBy  int64
	Pusher    string
	CreatedAt time.Time
}

// Review is a verdict on a change version.
type Review struct {
	ID         int64
	ChangeID   int64
	ReviewerID int64
	Reviewer   string
	Verdict    string
	Body       string
	Version    int
	HeadRev    string
	Counts     bool
	CreatedAt  time.Time
}

const changeCols = `c.id, c.repo_id, c.number, c.author_id, u.name, c.title, c.body, c.topic, c.target_branch, c.state, c.version, c.head_rev, c.base_rev, c.merged_rev,
	coalesce(c.merged_by, 0), coalesce(c.closed_by, 0), c.created_at, c.updated_at, c.merged_at, c.closed_at`
const changeFrom = ` FROM changes c JOIN users u ON u.id = c.author_id `

func scanChange(row interface{ Scan(...any) error }) (*Change, error) {
	var ch Change
	var cr, up string
	var ma, ca sql.NullString
	if err := row.Scan(&ch.ID, &ch.RepoID, &ch.Number, &ch.AuthorID, &ch.Author, &ch.Title, &ch.Body, &ch.Topic, &ch.TargetBranch, &ch.State, &ch.Version,
		&ch.HeadRev, &ch.BaseRev, &ch.MergedRev, &ch.MergedBy, &ch.ClosedBy, &cr, &up, &ma, &ca); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ch.CreatedAt, ch.UpdatedAt, ch.MergedAt, ch.ClosedAt = ParseTime(cr), ParseTime(up), nullTime(ma), nullTime(ca)
	return &ch, nil
}

// ChangeByID loads a change.
func (s *Store) ChangeByID(ctx context.Context, id int64) (*Change, error) {
	return scanChange(s.db.QueryRowContext(ctx, `SELECT `+changeCols+changeFrom+`WHERE c.id = ?`, id))
}

// ChangeByNumber loads a change by repository and number.
func (s *Store) ChangeByNumber(ctx context.Context, repoID, number int64) (*Change, error) {
	return scanChange(s.db.QueryRowContext(ctx, `SELECT `+changeCols+changeFrom+`WHERE c.repo_id = ? AND c.number = ?`, repoID, number))
}

// OpenChangeByTopic finds an open (non-terminal) change of an author by topic and target.
func (s *Store) OpenChangeByTopic(ctx context.Context, repoID, authorID int64, target, topic string) (*Change, error) {
	return scanChange(s.db.QueryRowContext(ctx, `SELECT `+changeCols+changeFrom+`WHERE c.repo_id = ? AND c.author_id = ? AND c.target_branch = ? AND c.topic = ? AND c.state NOT IN ('merged', 'closed') ORDER BY c.id DESC LIMIT 1`,
		repoID, authorID, target, topic))
}

// CountOpenChangesByAuthor counts non-terminal changes of a user in a repo.
func (s *Store) CountOpenChangesByAuthor(ctx context.Context, repoID, authorID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM changes WHERE repo_id = ? AND author_id = ? AND state NOT IN ('merged', 'closed')`, repoID, authorID).Scan(&n)
	return n, err
}

// ListChanges lists changes by state filter: "open" (all non-terminal), "merged", "closed", "" (all).
func (s *Store) ListChanges(ctx context.Context, repoID int64, filter string, limit int) ([]*Change, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + changeCols + changeFrom + `WHERE c.repo_id = ?`
	args := []any{repoID}
	switch filter {
	case "open":
		q += ` AND c.state NOT IN ('merged', 'closed')`
	case "merged", "closed":
		q += ` AND c.state = ?`
		args = append(args, filter)
	}
	q += ` ORDER BY c.updated_at DESC, c.number DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Change
	for rows.Next() {
		ch, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// CountChanges returns open/merged/closed counts.
func (s *Store) CountChanges(ctx context.Context, repoID int64) (open, merged, closed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(state NOT IN ('merged','closed')), 0), coalesce(sum(state = 'merged'), 0), coalesce(sum(state = 'closed'), 0) FROM changes WHERE repo_id = ?`, repoID).Scan(&open, &merged, &closed)
	return
}

// NewChangeVersion records a pushed version, creating the change when
// ch.ID is zero. It runs in one transaction and returns the refreshed change
// and version.
func (s *Store) NewChangeVersion(ctx context.Context, ch *Change, head, base string, commits int, pusherID int64) (*Change, *ChangeVersion, error) {
	var vid int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		now := Now()
		if ch.ID == 0 {
			var n int64
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(number), 0) + 1 FROM changes WHERE repo_id = ?`, ch.RepoID).Scan(&n); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO changes (repo_id, number, author_id, title, body, topic, target_branch, state, version, head_rev, base_rev, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'open', 1, ?, ?, ?, ?)`, ch.RepoID, n, ch.AuthorID, ch.Title, ch.Body, ch.Topic, ch.TargetBranch, head, base, now, now)
			if err != nil {
				return err
			}
			ch.ID, _ = res.LastInsertId()
			ch.Number, ch.Version = n, 1
		} else {
			ch.Version++
			if _, err := tx.ExecContext(ctx, `UPDATE changes SET version = ?, head_rev = ?, base_rev = ?, state = 'open', closed_by = NULL, closed_at = NULL, updated_at = ? WHERE id = ?`,
				ch.Version, head, base, now, ch.ID); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO change_versions (change_id, number, head_rev, base_rev, commits, pushed_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.Version, head, base, commits, pusherID, now)
		if err != nil {
			return err
		}
		vid, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `UPDATE repositories SET updated_at = ? WHERE id = ?`, now, ch.RepoID)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	fresh, err := s.ChangeByID(ctx, ch.ID)
	if err != nil {
		return nil, nil, err
	}
	versions, err := s.ListChangeVersions(ctx, ch.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range versions {
		if v.ID == vid {
			return fresh, v, nil
		}
	}
	return fresh, nil, ErrNotFound
}

// ListChangeVersions lists versions oldest first.
func (s *Store) ListChangeVersions(ctx context.Context, changeID int64) ([]*ChangeVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id, v.change_id, v.number, v.head_rev, v.base_rev, v.commits, v.pushed_by, u.name, v.created_at FROM change_versions v JOIN users u ON u.id = v.pushed_by WHERE v.change_id = ? ORDER BY v.number`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ChangeVersion
	for rows.Next() {
		var v ChangeVersion
		var c string
		if err := rows.Scan(&v.ID, &v.ChangeID, &v.Number, &v.HeadRev, &v.BaseRev, &v.Commits, &v.PushedBy, &v.Pusher, &c); err != nil {
			return nil, err
		}
		v.CreatedAt = ParseTime(c)
		out = append(out, &v)
	}
	return out, rows.Err()
}

// UpdateChangeText edits title and body.
func (s *Store) UpdateChangeText(ctx context.Context, id int64, title, body string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE changes SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, Now(), id)
	return err
}

// SetChangeState sets a non-terminal or closed state.
func (s *Store) SetChangeState(ctx context.Context, id int64, state string, byUser int64) error {
	now := Now()
	if state == "closed" {
		_, err := s.db.ExecContext(ctx, `UPDATE changes SET state = 'closed', closed_by = ?, closed_at = ?, updated_at = ? WHERE id = ?`, byUser, now, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE changes SET state = ?, closed_by = NULL, closed_at = NULL, updated_at = ? WHERE id = ?`, state, now, id)
	return err
}

// MarkChangeMerged records a merge.
func (s *Store) MarkChangeMerged(ctx context.Context, id int64, mergedRev string, byUser int64) error {
	now := Now()
	_, err := s.db.ExecContext(ctx, `UPDATE changes SET state = 'merged', merged_rev = ?, merged_by = ?, merged_at = ?, updated_at = ? WHERE id = ?`, mergedRev, byUser, now, now, id)
	return err
}

// AddReview records a review.
func (s *Store) AddReview(ctx context.Context, r *Review) (*Review, error) {
	now := Now()
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO reviews (change_id, reviewer_id, verdict, body, version, head_rev, counts, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ChangeID, r.ReviewerID, r.Verdict, r.Body, r.Version, r.HeadRev, r.Counts, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `UPDATE changes SET updated_at = ? WHERE id = ?`, now, r.ChangeID)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.ID, r.CreatedAt = id, ParseTime(now)
	return r, nil
}

// ListReviews lists reviews of a change, newest first.
func (s *Store) ListReviews(ctx context.Context, changeID int64) ([]*Review, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.change_id, r.reviewer_id, u.name, r.verdict, r.body, r.version, r.head_rev, r.counts, r.created_at FROM reviews r JOIN users u ON u.id = r.reviewer_id WHERE r.change_id = ? ORDER BY r.id DESC`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Review
	for rows.Next() {
		var r Review
		var c string
		if err := rows.Scan(&r.ID, &r.ChangeID, &r.ReviewerID, &r.Reviewer, &r.Verdict, &r.Body, &r.Version, &r.HeadRev, &r.Counts, &c); err != nil {
			return nil, err
		}
		r.CreatedAt = ParseTime(c)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ComputeChangeState derives the state of the current version from the
// latest counted review of each reviewer.
func (s *Store) ComputeChangeState(ctx context.Context, changeID int64, version int) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT verdict FROM reviews r WHERE r.change_id = ? AND r.version = ? AND r.counts = 1 AND r.id = (SELECT max(id) FROM reviews x WHERE x.change_id = r.change_id AND x.version = r.version AND x.reviewer_id = r.reviewer_id)`, changeID, version)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	approved := false
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		switch v {
		case "request-changes":
			return "changes-requested", nil
		case "approve":
			approved = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if approved {
		return "approved", nil
	}
	return "open", nil
}

// AddChangeComment appends a comment on a change with its version.
func (s *Store) AddChangeComment(ctx context.Context, repoID, changeID, authorID int64, version int, body string) (*Comment, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		now := Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO comments (repo_id, target_kind, target_id, author_id, body, version, created_at, updated_at) VALUES (?, 'change', ?, ?, ?, ?, ?, ?)`,
			repoID, changeID, authorID, body, version, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return s.touchTarget(ctx, tx, "change", changeID)
	})
	if err != nil {
		return nil, err
	}
	return s.CommentByID(ctx, id)
}

// CommentVersion returns the change version a comment was made on.
func (s *Store) CommentVersion(ctx context.Context, id int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM comments WHERE id = ?`, id).Scan(&v)
	return v, err
}
