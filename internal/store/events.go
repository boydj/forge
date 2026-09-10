package store

import (
	"context"
	"encoding/json"
	"time"
)

// Event is one activity/replication log entry.
type Event struct {
	ID        int64
	Kind      string
	RepoID    int64
	UserID    int64
	Subject   string
	Path      string
	Payload   json.RawMessage
	Node      string
	CreatedAt time.Time
	// Joined for rendering.
	Owner    string
	RepoName string
	UserName string
}

// Event kinds.
const (
	EventRepoCreate    = "repo.create"
	EventRepoUpdate    = "repo.update"
	EventRepoArchive   = "repo.archive"
	EventRepoDelete    = "repo.delete"
	EventPush          = "push"
	EventIssueOpen     = "issue.open"
	EventIssueClose    = "issue.close"
	EventIssueReopen   = "issue.reopen"
	EventIssueEdit     = "issue.edit"
	EventComment       = "comment"
	EventChangeOpen    = "change.open"
	EventChangeUpdate  = "change.update"
	EventChangeMerge   = "change.merge"
	EventChangeClose   = "change.close"
	EventReview        = "review"
	EventRelease       = "release"
	EventReleaseAsset  = "release.asset"
	EventUserCreate    = "user.create"
	EventUserKeyAdd    = "user.key.add"
	EventUserCertAdd   = "user.cert.add"
	EventAdminAction   = "admin"
	EventReplicaStatus = "replica.status"
)

// AddEvent appends an event. Payload may be nil.
func (s *Store) AddEvent(ctx context.Context, e *Event) (int64, error) {
	payload := "{}"
	if len(e.Payload) > 0 {
		payload = string(e.Payload)
	}
	var repo, user any
	if e.RepoID != 0 {
		repo = e.RepoID
	}
	if e.UserID != 0 {
		user = e.UserID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO events (kind, repo_id, user_id, subject, path, payload, node, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Kind, repo, user, e.Subject, e.Path, payload, s.node, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EventQuery selects events for feeds.
type EventQuery struct {
	RepoID int64
	UserID int64
	// Kinds restricts to these kinds (nil: all).
	Kinds []string
	// Viewer controls private-repo visibility (0: anonymous).
	Viewer  int64
	AfterID int64
	Limit   int
}

// Events lists events newest first, hiding those of private repositories the
// viewer cannot see.
func (s *Store) Events(ctx context.Context, q EventQuery) ([]*Event, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	sqlq := `SELECT e.id, e.kind, coalesce(e.repo_id, 0), coalesce(e.user_id, 0), e.subject, e.path, e.payload, e.node, e.created_at,
		coalesce(o.name, ''), coalesce(r.name, ''), coalesce(u.name, '')
		FROM events e
		LEFT JOIN repositories r ON r.id = e.repo_id
		LEFT JOIN users o ON o.id = r.owner_id
		LEFT JOIN users u ON u.id = e.user_id
		WHERE (e.repo_id IS NULL OR (r.deleted_at IS NULL AND (r.private = 0 OR r.owner_id = ?1
			OR EXISTS (SELECT 1 FROM collaborators c WHERE c.repo_id = r.id AND c.user_id = ?1)
			OR EXISTS (SELECT 1 FROM users a WHERE a.id = ?1 AND a.admin = 1))))`
	args := []any{q.Viewer}
	if q.RepoID != 0 {
		args = append(args, q.RepoID)
		sqlq += ` AND e.repo_id = ?` + itoa(len(args))
	}
	if q.UserID != 0 {
		args = append(args, q.UserID)
		sqlq += ` AND e.user_id = ?` + itoa(len(args))
	}
	if len(q.Kinds) > 0 {
		sqlq += ` AND e.kind IN (`
		for i, k := range q.Kinds {
			if i > 0 {
				sqlq += `,`
			}
			args = append(args, k)
			sqlq += `?` + itoa(len(args))
		}
		sqlq += `)`
	}
	if q.AfterID != 0 {
		args = append(args, q.AfterID)
		sqlq += ` AND e.id > ?` + itoa(len(args))
		sqlq += ` ORDER BY e.id ASC`
	} else {
		sqlq += ` ORDER BY e.id DESC`
	}
	args = append(args, q.Limit)
	sqlq += ` LIMIT ?` + itoa(len(args))
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var payload, created string
		if err := rows.Scan(&e.ID, &e.Kind, &e.RepoID, &e.UserID, &e.Subject, &e.Path, &payload, &e.Node, &created, &e.Owner, &e.RepoName, &e.UserName); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		e.CreatedAt = ParseTime(created)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// LastEventID returns the newest event id.
func (s *Store) LastEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM events`).Scan(&id)
	return id, err
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
