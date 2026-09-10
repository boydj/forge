package store

// Replication support: per-origin event cursors, replicated event insertion,
// whole-row snapshots of repository and global metadata, and replica
// bookkeeping. See docs/replication.md. Rows exchanged between nodes are
// generic column maps so that the wire format follows the schema without a
// second set of types; column names are validated against PRAGMA table_info
// before they are interpolated into SQL.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalEvents returns events authored on this node with id > after, oldest
// first, for peers to pull. Replicated events (origin_id set) are excluded so
// that each node only ever serves its own log.
func (s *Store) LocalEvents(ctx context.Context, after int64, limit int) ([]*Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, coalesce(repo_id, 0), coalesce(user_id, 0), subject, path, payload, node, created_at
		FROM events WHERE node = ? AND origin_id IS NULL AND id > ? ORDER BY id ASC LIMIT ?`, s.node, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var payload, created string
		if err := rows.Scan(&e.ID, &e.Kind, &e.RepoID, &e.UserID, &e.Subject, &e.Path, &payload, &e.Node, &created); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		e.CreatedAt = ParseTime(created)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// LastLocalEventID returns the newest event id authored on this node.
func (s *Store) LastLocalEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM events WHERE node = ? AND origin_id IS NULL`, s.node).Scan(&id)
	return id, err
}

// ReplCursor returns the highest origin event id applied from node.
func (s *Store) ReplCursor(ctx context.Context, node string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT last_origin_id FROM repl_cursors WHERE node = ?`, node).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// ReplCursors returns every origin cursor.
func (s *Store) ReplCursors(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node, last_origin_id FROM repl_cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var n string
		var id int64
		if err := rows.Scan(&n, &id); err != nil {
			return nil, err
		}
		out[n] = id
	}
	return out, rows.Err()
}

// ApplyRemoteEvents inserts events pulled from origin, preserving their
// node, timestamps and origin ids, and advances the origin cursor in the same
// transaction. Events already applied (same node and origin id) are skipped,
// so re-applying a batch is harmless. Repository and user references that are
// unknown locally are stored as NULL rather than failing: the event log must
// never block on the order in which other tables replicate.
func (s *Store) ApplyRemoteEvents(ctx context.Context, origin string, events []*Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var last int64
		for _, e := range events {
			if e.ID <= 0 || e.Node != origin {
				return fmt.Errorf("store: event %d from %q claims node %q", e.ID, origin, e.Node)
			}
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
			created := e.CreatedAt.UTC().Format(time.RFC3339)
			if e.CreatedAt.IsZero() {
				created = Now()
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events (kind, repo_id, user_id, subject, path, payload, node, created_at, origin_id)
				VALUES (?, (SELECT id FROM repositories WHERE id = ?), (SELECT id FROM users WHERE id = ?), ?, ?, ?, ?, ?, ?)`,
				e.Kind, repo, user, e.Subject, e.Path, payload, origin, created, e.ID); err != nil {
				return err
			}
			if e.ID > last {
				last = e.ID
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO repl_cursors (node, last_origin_id, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(node) DO UPDATE SET last_origin_id = max(last_origin_id, excluded.last_origin_id), updated_at = excluded.updated_at`, origin, last, Now())
		return err
	})
}

// ReplicatedEventCount counts events applied from origin (tests, status).
func (s *Store) ReplicatedEventCount(ctx context.Context, origin string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE node = ? AND origin_id IS NOT NULL`, origin).Scan(&n)
	return n, err
}

// ReposForReplication lists every repository record including soft-deleted
// ones (purged rows are gone on the leader too), oldest first.
func (s *Store) ReposForReplication(ctx context.Context) ([]*Repo, error) {
	return s.queryRepos(ctx, `SELECT `+repoCols+repoFrom+`ORDER BY r.id`)
}

// UpsertRepo writes a complete repository row keyed by the leader-assigned
// id. Used only by replication; ids are never chosen locally for repositories
// this node does not lead.
func (s *Store) UpsertRepo(ctx context.Context, r *Repo) error {
	if r.ID <= 0 {
		return errors.New("store: upsert repo without id")
	}
	var pushed, deleted any
	if !r.PushedAt.IsZero() {
		pushed = r.PushedAt.UTC().Format(time.RFC3339)
	}
	if !r.DeletedAt.IsZero() {
		deleted = r.DeletedAt.UTC().Format(time.RFC3339)
	}
	created, updated := r.CreatedAt.UTC().Format(time.RFC3339), r.UpdatedAt.UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO repositories (id, owner_id, name, description, private, archived, default_branch, vcs, leader_node, size_bytes, created_at, updated_at, pushed_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner_id = excluded.owner_id, name = excluded.name, description = excluded.description,
			private = excluded.private, archived = excluded.archived, default_branch = excluded.default_branch, vcs = excluded.vcs,
			leader_node = excluded.leader_node, size_bytes = excluded.size_bytes, created_at = excluded.created_at,
			updated_at = excluded.updated_at, pushed_at = excluded.pushed_at, deleted_at = excluded.deleted_at`,
		r.ID, r.OwnerID, r.Name, r.Description, r.Private, r.Archived, r.DefaultBranch, r.VCS, r.LeaderNode, r.SizeBytes, created, updated, pushed, deleted)
	return err
}

// SetRepoLeader changes the leader of a repository.
func (s *Store) SetRepoLeader(ctx context.Context, id int64, node string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET leader_node = ?, updated_at = ? WHERE id = ?`, node, Now(), id)
	return err
}

// CountLeaderRepos counts non-deleted repositories led by node.
func (s *Store) CountLeaderRepos(ctx context.Context, node string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM repositories WHERE leader_node = ? AND deleted_at IS NULL`, node).Scan(&n)
	return n, err
}

// Row is one table row as column name to value. Values are int64, float64,
// string, []byte or nil.
type Row map[string]any

// TableRows is a set of rows per table.
type TableRows map[string][]Row

// repoTable describes how the rows of a table belonging to one repository
// are selected. Order matters for inserts (parents first); deletes run in
// reverse.
type repoTable struct {
	name  string
	where string
}

var repoTables = []repoTable{
	{"collaborators", "repo_id = ?"},
	{"issues", "repo_id = ?"},
	{"changes", "repo_id = ?"},
	{"change_versions", "change_id IN (SELECT id FROM changes WHERE repo_id = ?)"},
	{"comments", "repo_id = ?"},
	{"reviews", "change_id IN (SELECT id FROM changes WHERE repo_id = ?)"},
	{"releases", "repo_id = ?"},
	{"release_assets", "release_id IN (SELECT id FROM releases WHERE repo_id = ?)"},
}

var globalTables = []repoTable{
	{"users", "1 = 1"},
	{"certificates", "1 = 1"},
	{"ssh_keys", "1 = 1"},
}

// RepoMetadata snapshots every per-repository metadata row (collaborators,
// issues, changes, comments, reviews, releases, release assets).
func (s *Store) RepoMetadata(ctx context.Context, repoID int64) (TableRows, error) {
	out := TableRows{}
	for _, t := range repoTables {
		if ok, err := s.hasTable(ctx, t.name); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		rows, err := s.dumpRows(ctx, t.name, t.where, repoID)
		if err != nil {
			return nil, err
		}
		out[t.name] = rows
	}
	return out, nil
}

// ApplyRepoMetadata replaces the local metadata rows of a repository with a
// snapshot from its leader, atomically. Ids and timestamps are preserved.
func (s *Store) ApplyRepoMetadata(ctx context.Context, repoID int64, md TableRows) error {
	for name := range md {
		if !knownTable(repoTables, name) {
			return fmt.Errorf("store: unknown replicated table %q", name)
		}
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for i := len(repoTables) - 1; i >= 0; i-- {
			t := repoTables[i]
			if ok, err := s.hasTable(ctx, t.name); err != nil || !ok {
				if err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.name+` WHERE `+t.where, repoID); err != nil {
				return err
			}
		}
		for _, t := range repoTables {
			for _, row := range md[t.name] {
				if rid, ok := row["repo_id"]; ok && toInt64(rid) != repoID {
					return fmt.Errorf("store: %s row for repository %v in snapshot of %d", t.name, rid, repoID)
				}
				if err := s.upsertRow(ctx, tx, t.name, row); err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
			}
		}
		return nil
	})
}

// GlobalMetadata snapshots users, certificates and SSH keys.
func (s *Store) GlobalMetadata(ctx context.Context) (TableRows, error) {
	out := TableRows{}
	for _, t := range globalTables {
		rows, err := s.dumpRows(ctx, t.name, t.where)
		if err != nil {
			return nil, err
		}
		out[t.name] = rows
	}
	return out, nil
}

// ApplyGlobalMetadata upserts users, certificates and SSH keys from the
// metadata leader. Users are never deleted locally (repositories reference
// them); certificates and keys absent from the snapshot are removed.
func (s *Store) ApplyGlobalMetadata(ctx context.Context, md TableRows) error {
	for name := range md {
		if !knownTable(globalTables, name) {
			return fmt.Errorf("store: unknown replicated table %q", name)
		}
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for _, t := range globalTables {
			rows := md[t.name]
			for _, row := range rows {
				if err := s.upsertRow(ctx, tx, t.name, row); err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
			}
			if t.name == "users" {
				continue
			}
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, fmt.Sprint(toInt64(row["id"])))
			}
			q := `DELETE FROM ` + t.name
			if len(ids) > 0 {
				q += ` WHERE id NOT IN (` + strings.Join(ids, ",") + `)`
			}
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

func knownTable(ts []repoTable, name string) bool {
	for _, t := range ts {
		if t.name == name {
			return true
		}
	}
	return false
}

// dumpRows selects * from a known table and returns column maps.
func (s *Store) dumpRows(ctx context.Context, table, where string, args ...any) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM `+table+` WHERE `+where+` ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []Row{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			r[c] = v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

var tableColumns sync.Map // table -> map[string]bool

// hasTable reports whether a table exists in the local schema (replicated
// tables may lag behind a peer's migrations).
func (s *Store) hasTable(ctx context.Context, table string) (bool, error) {
	if _, ok := tableColumns.Load(table); ok {
		return true, nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// columns returns the column set of a table (cached; the schema is fixed for
// the life of the process).
func (s *Store) columns(ctx context.Context, table string) (map[string]bool, error) {
	if v, ok := tableColumns.Load(table); ok {
		return v.(map[string]bool), nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("store: no such table %q", table)
	}
	tableColumns.Store(table, cols)
	return cols, nil
}

// upsertRow inserts a row by primary key id (or the composite key of
// collaborators), updating every supplied column on conflict. Unknown columns
// are rejected; missing columns take their schema defaults.
func (s *Store) upsertRow(ctx context.Context, tx *sql.Tx, table string, row Row) error {
	known, err := s.columns(ctx, table)
	if err != nil {
		return err
	}
	cols := make([]string, 0, len(row))
	for c := range row {
		if !known[c] {
			return fmt.Errorf("unknown column %q", c)
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		return errors.New("empty row")
	}
	// Deterministic order keeps prepared statements cacheable and logs stable.
	sort.Strings(cols)
	key := "id"
	if table == "collaborators" {
		key = "repo_id, user_id"
	}
	args := make([]any, 0, len(cols))
	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		args = append(args, normalise(row[c]))
		sets = append(sets, c+" = excluded."+c)
	}
	q := `INSERT INTO ` + table + ` (` + strings.Join(cols, ", ") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)
		ON CONFLICT(` + key + `) DO UPDATE SET ` + strings.Join(sets, ", ")
	_, err = tx.ExecContext(ctx, q, args...)
	return err
}

// normalise converts JSON-decoded values (json.Number, float64) to what the
// driver expects.
func normalise(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	}
	return v
}

func toInt64(v any) int64 {
	switch x := normalise(v).(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

// Replica is one repo_replicas row joined with its repository.
type Replica struct {
	RepoID          int64
	Owner           string
	RepoName        string
	LeaderNode      string
	Node            string
	LastEventID     int64
	LastSyncedAt    time.Time
	Status          string
	Detail          string
	LeaderUpdatedAt string
	LeaderPushedAt  string
}

// Replica statuses.
const (
	ReplicaOK      = "ok"
	ReplicaError   = "error"
	ReplicaPending = "pending"
	ReplicaResync  = "resync"
)

const replicaCols = `p.repo_id, coalesce(u.name, ''), coalesce(r.name, ''), coalesce(r.leader_node, ''), p.node, p.last_event_id, p.last_synced_at, p.status, p.detail, p.leader_updated_at, p.leader_pushed_at`
const replicaFrom = ` FROM repo_replicas p LEFT JOIN repositories r ON r.id = p.repo_id LEFT JOIN users u ON u.id = r.owner_id `

func scanReplica(row interface{ Scan(...any) error }) (*Replica, error) {
	var p Replica
	var synced sql.NullString
	if err := row.Scan(&p.RepoID, &p.Owner, &p.RepoName, &p.LeaderNode, &p.Node, &p.LastEventID, &synced, &p.Status, &p.Detail, &p.LeaderUpdatedAt, &p.LeaderPushedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.LastSyncedAt = nullTime(synced)
	return &p, nil
}

// SetReplica records the outcome of a sync of repoID on node. A successful
// sync stamps last_synced_at; a failure keeps the previous timestamps so the
// next cycle retries the same work.
func (s *Store) SetReplica(ctx context.Context, p *Replica) error {
	if p.Status == ReplicaOK {
		_, err := s.db.ExecContext(ctx, `INSERT INTO repo_replicas (repo_id, node, last_event_id, last_synced_at, status, detail, leader_updated_at, leader_pushed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, node) DO UPDATE SET last_event_id = excluded.last_event_id, last_synced_at = excluded.last_synced_at,
				status = excluded.status, detail = excluded.detail, leader_updated_at = excluded.leader_updated_at, leader_pushed_at = excluded.leader_pushed_at`,
			p.RepoID, p.Node, p.LastEventID, Now(), p.Status, p.Detail, p.LeaderUpdatedAt, p.LeaderPushedAt)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_replicas (repo_id, node, last_event_id, status, detail)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, node) DO UPDATE SET status = excluded.status, detail = excluded.detail`,
		p.RepoID, p.Node, p.LastEventID, p.Status, p.Detail)
	return err
}

// ReplicaFor loads one replica row.
func (s *Store) ReplicaFor(ctx context.Context, repoID int64, node string) (*Replica, error) {
	return scanReplica(s.db.QueryRowContext(ctx, `SELECT `+replicaCols+replicaFrom+`WHERE p.repo_id = ? AND p.node = ?`, repoID, node))
}

// Replicas lists every replica row, by repository then node.
func (s *Store) Replicas(ctx context.Context) ([]*Replica, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+replicaCols+replicaFrom+`ORDER BY p.repo_id, p.node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Replica
	for rows.Next() {
		p, err := scanReplica(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NodeInfo is a known cluster node.
type NodeInfo struct {
	Name        string
	ControlAddr string
	Version     string
	LastSeenAt  time.Time
}

// UpsertNode records a peer's address and version and stamps last_seen_at.
func (s *Store) UpsertNode(ctx context.Context, name, addr, version string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO nodes (name, control_addr, version, last_seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET control_addr = excluded.control_addr, version = excluded.version, last_seen_at = excluded.last_seen_at`,
		name, addr, version, Now())
	return err
}

// Nodes lists known nodes by name.
func (s *Store) Nodes(ctx context.Context) ([]*NodeInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, control_addr, version, last_seen_at FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NodeInfo
	for rows.Next() {
		var n NodeInfo
		var seen sql.NullString
		if err := rows.Scan(&n.Name, &n.ControlAddr, &n.Version, &seen); err != nil {
			return nil, err
		}
		n.LastSeenAt = nullTime(seen)
		out = append(out, &n)
	}
	return out, rows.Err()
}
