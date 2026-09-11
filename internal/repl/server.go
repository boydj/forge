package repl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
)

// Wire types. Times are RFC 3339 UTC strings ("" for unset) so that replicas
// store exactly what the leader has.

// StatusResponse is /v1/status.
type StatusResponse struct {
	Node           string `json:"node"`
	Version        string `json:"version"`
	MetadataLeader string `json:"metadata_leader"`
	LeaderRepos    int    `json:"leader_repos"`
	LastEventID    int64  `json:"last_event_id"`
	Time           string `json:"time"`
	// Health is the node's health and announcement state, present when it
	// runs a health controller (Options.Health). The fleet status page
	// (/status/) is built from these.
	Health *HealthStatus `json:"health,omitempty"`
	// StartedAt is when the process started (RFC 3339 UTC).
	StartedAt string `json:"started_at,omitempty"`
	// ReplicaLag is the largest number of events this node was behind any
	// leader it follows at its last sync; -1 before the first sync.
	ReplicaLag int64 `json:"replica_lag"`
}

// HealthStatus is the wire form of health.Status.
type HealthStatus struct {
	Healthy bool          `json:"healthy"`
	Detail  string        `json:"detail,omitempty"`
	State   string        `json:"state"` // announced, drained, withdrawn
	Manual  bool          `json:"manual,omitempty"`
	Checks  []CheckStatus `json:"checks,omitempty"`
}

// CheckStatus is one health check's last result.
type CheckStatus struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// wireEvent is one event on the wire.
type wireEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	RepoID    int64           `json:"repo_id,omitempty"`
	UserID    int64           `json:"user_id,omitempty"`
	Subject   string          `json:"subject"`
	Path      string          `json:"path"`
	Payload   json.RawMessage `json:"payload"`
	Node      string          `json:"node"`
	CreatedAt string          `json:"created_at"`
}

func toWireEvent(e *store.Event) wireEvent {
	return wireEvent{ID: e.ID, Kind: e.Kind, RepoID: e.RepoID, UserID: e.UserID, Subject: e.Subject, Path: e.Path,
		Payload: e.Payload, Node: e.Node, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339)}
}

func fromWireEvent(w wireEvent) *store.Event {
	return &store.Event{ID: w.ID, Kind: w.Kind, RepoID: w.RepoID, UserID: w.UserID, Subject: w.Subject, Path: w.Path,
		Payload: w.Payload, Node: w.Node, CreatedAt: store.ParseTime(w.CreatedAt)}
}

// wireRepo is a repository record on the wire.
type wireRepo struct {
	ID            int64  `json:"id"`
	OwnerID       int64  `json:"owner_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	VCS           string `json:"vcs"`
	LeaderNode    string `json:"leader_node"`
	SizeBytes     int64  `json:"size_bytes"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	PushedAt      string `json:"pushed_at,omitempty"`
	DeletedAt     string `json:"deleted_at,omitempty"`
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toWireRepo(r *store.Repo) wireRepo {
	return wireRepo{ID: r.ID, OwnerID: r.OwnerID, Owner: r.Owner, Name: r.Name, Description: r.Description, Private: r.Private,
		Archived: r.Archived, DefaultBranch: r.DefaultBranch, VCS: r.VCS, LeaderNode: r.LeaderNode, SizeBytes: r.SizeBytes,
		CreatedAt: fmtTime(r.CreatedAt), UpdatedAt: fmtTime(r.UpdatedAt), PushedAt: fmtTime(r.PushedAt), DeletedAt: fmtTime(r.DeletedAt)}
}

func fromWireRepo(w wireRepo) *store.Repo {
	return &store.Repo{ID: w.ID, OwnerID: w.OwnerID, Owner: w.Owner, Name: w.Name, Description: w.Description, Private: w.Private,
		Archived: w.Archived, DefaultBranch: w.DefaultBranch, VCS: w.VCS, LeaderNode: w.LeaderNode, SizeBytes: w.SizeBytes,
		CreatedAt: store.ParseTime(w.CreatedAt), UpdatedAt: store.ParseTime(w.UpdatedAt), PushedAt: store.ParseTime(w.PushedAt), DeletedAt: store.ParseTime(w.DeletedAt)}
}

// wireRef is one ref in /v1/repos/{id}/state.
type wireRef struct {
	Name   string `json:"name"`
	Kind   int    `json:"kind"`
	Object string `json:"object"`
}

// RepoState is /v1/repos/{id}/state: what this node holds for a repository.
type RepoState struct {
	RepoID          int64     `json:"repo_id"`
	Node            string    `json:"node"`
	LeaderNode      string    `json:"leader_node"`
	UpdatedAt       string    `json:"updated_at"`
	Refs            []wireRef `json:"refs"`
	ReplicaStatus   string    `json:"replica_status,omitempty"`
	LeaderUpdatedAt string    `json:"leader_updated_at,omitempty"`
	LeaderPushedAt  string    `json:"leader_pushed_at,omitempty"`
}

type notifyRequest struct {
	RepoID int64 `json:"repo_id"`
}

// Handler returns the control-plane HTTP handler. Every route requires
// authentication.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", n.withAuth(n.handleStatus))
	mux.HandleFunc("GET /v1/events", n.withAuth(n.handleEvents))
	mux.HandleFunc("GET /v1/repos", n.withAuth(n.handleRepos))
	mux.HandleFunc("GET /v1/repos/{id}/metadata", n.withAuth(n.handleRepoMetadata))
	mux.HandleFunc("GET /v1/repos/{id}/state", n.withAuth(n.handleRepoState))
	mux.HandleFunc("GET /v1/repos/{id}/assets/{tag}/{name}", n.withAuth(n.handleAsset))
	mux.HandleFunc("GET /v1/users", n.withAuth(n.handleUsers))
	mux.HandleFunc("POST /v1/notify", n.withAuth(n.handleNotify))
	mux.HandleFunc("POST /v1/forward", n.withAuth(n.handleForward))
	mux.HandleFunc("GET /v1/git/{owner}/{repo}/info/refs", n.withAuth(n.handleInfoRefs))
	mux.HandleFunc("POST /v1/git/{owner}/{repo}/git-upload-pack", n.withAuth(n.handleUploadPack))
	mux.HandleFunc("POST /v1/git/{owner}/{repo}/git-receive-pack", n.withAuth(func(w http.ResponseWriter, r *http.Request, _ string) {
		http.Error(w, "pushes are not accepted over the control plane", http.StatusForbidden)
	}))
	mux.HandleFunc("/", n.withAuth(func(w http.ResponseWriter, r *http.Request, _ string) {
		http.NotFound(w, r)
	}))
	return mux
}

func (n *Node) status(ctx context.Context) (StatusResponse, error) {
	led, err := n.opts.Store.CountLeaderRepos(ctx, n.opts.Name)
	if err != nil {
		return StatusResponse{}, err
	}
	last, err := n.opts.Store.LastLocalEventID(ctx)
	if err != nil {
		return StatusResponse{}, err
	}
	if n.opts.Metrics != nil {
		n.opts.Metrics.LeaderRepos.Set(float64(led))
	}
	st := StatusResponse{Node: n.opts.Name, Version: n.opts.Version, MetadataLeader: n.meta, LeaderRepos: led, LastEventID: last, Time: store.Now(),
		StartedAt: n.started.UTC().Format(time.RFC3339), ReplicaLag: n.maxLag()}
	if n.opts.Health != nil {
		st.Health = healthStatus(n.opts.Health())
	}
	return st, nil
}

func (n *Node) handleStatus(w http.ResponseWriter, r *http.Request, _ string) {
	st, err := n.status(r.Context())
	if err != nil {
		n.serverError(w, err)
		return
	}
	writeJSON(w, st)
}

func (n *Node) handleEvents(w http.ResponseWriter, r *http.Request, _ string) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxPeerLimit {
		limit = 500
	}
	evs, err := n.opts.Store.LocalEvents(r.Context(), after, limit)
	if err != nil {
		n.serverError(w, err)
		return
	}
	out := make([]wireEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, toWireEvent(e))
	}
	writeJSON(w, out)
}

func (n *Node) handleRepos(w http.ResponseWriter, r *http.Request, _ string) {
	repos, err := n.opts.Store.ReposForReplication(r.Context())
	if err != nil {
		n.serverError(w, err)
		return
	}
	out := make([]wireRepo, 0, len(repos))
	for _, rp := range repos {
		out = append(out, toWireRepo(rp))
	}
	writeJSON(w, out)
}

// repoFromRequest loads the repository named by the {id} path value.
func (n *Node) repoFromRequest(w http.ResponseWriter, r *http.Request) (*store.Repo, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad repository id", http.StatusBadRequest)
		return nil, false
	}
	rp, err := n.opts.Store.RepoByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		n.serverError(w, err)
		return nil, false
	}
	return rp, true
}

func (n *Node) handleRepoMetadata(w http.ResponseWriter, r *http.Request, _ string) {
	rp, ok := n.repoFromRequest(w, r)
	if !ok {
		return
	}
	if rp.LeaderNode != n.opts.Name {
		http.Error(w, "not the leader of this repository; leader is "+rp.LeaderNode, http.StatusConflict)
		return
	}
	md, err := n.opts.Store.RepoMetadata(r.Context(), rp.ID)
	if err != nil {
		n.serverError(w, err)
		return
	}
	writeJSON(w, md)
}

func (n *Node) repoState(ctx context.Context, rp *store.Repo) (*RepoState, error) {
	st := &RepoState{RepoID: rp.ID, Node: n.opts.Name, LeaderNode: rp.LeaderNode, UpdatedAt: fmtTime(rp.UpdatedAt), Refs: []wireRef{}}
	if repo, err := n.opts.Git.Open(n.RepoPath(rp.Owner, rp.Name)); err == nil {
		refs, err := repo.Refs(ctx)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			st.Refs = append(st.Refs, wireRef{Name: ref.Name, Kind: int(ref.Kind), Object: string(ref.Object)})
		}
	}
	if p, err := n.opts.Store.ReplicaFor(ctx, rp.ID, n.opts.Name); err == nil {
		st.ReplicaStatus, st.LeaderUpdatedAt, st.LeaderPushedAt = p.Status, p.LeaderUpdatedAt, p.LeaderPushedAt
	}
	return st, nil
}

func (n *Node) handleRepoState(w http.ResponseWriter, r *http.Request, _ string) {
	rp, ok := n.repoFromRequest(w, r)
	if !ok {
		return
	}
	st, err := n.repoState(r.Context(), rp)
	if err != nil {
		n.serverError(w, err)
		return
	}
	writeJSON(w, st)
}

func (n *Node) handleUsers(w http.ResponseWriter, r *http.Request, _ string) {
	if n.meta != n.opts.Name {
		http.Error(w, "not the metadata leader; leader is "+n.meta, http.StatusConflict)
		return
	}
	md, err := n.opts.Store.GlobalMetadata(r.Context())
	if err != nil {
		n.serverError(w, err)
		return
	}
	writeJSON(w, md)
}

func (n *Node) handleNotify(w http.ResponseWriter, r *http.Request, peer string) {
	var req notifyRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	select {
	case n.wake <- req.RepoID:
	default: // a sync is already queued; it will pick the change up
	}
	n.log.Debug("notified", "peer", peer, "repo", req.RepoID)
	w.WriteHeader(http.StatusAccepted)
}

func (n *Node) serverError(w http.ResponseWriter, err error) {
	n.log.Error("control request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// refsEqual compares two ref sets by name, kind and object id.
func refsEqual(a []vcs.Ref, b []wireRef) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]string, len(a))
	for _, r := range a {
		m[strconv.Itoa(int(r.Kind))+":"+r.Name] = string(r.Object)
	}
	for _, r := range b {
		if m[strconv.Itoa(r.Kind)+":"+r.Name] != r.Object {
			return false
		}
	}
	return true
}
