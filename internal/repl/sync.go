package repl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"as215520.net/forge/internal/store"
)

// Replica worker. Every SyncInterval (or sooner when a leader notifies us)
// one cycle runs: pull global metadata from the metadata leader, then for
// each peer pull its event log (per-origin cursor), its repository records,
// and for each repository it leads fetch git refs and re-snapshot metadata
// when something changed. All steps are idempotent; a failure is recorded on
// the affected repo_replicas row and retried next cycle.

const eventBatch = 500

// Run polls until ctx is cancelled.
func (n *Node) Run(ctx context.Context) {
	t := time.NewTicker(n.opts.SyncInterval)
	defer t.Stop()
	n.runCycle(ctx, 0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.runCycle(ctx, 0)
		case id := <-n.wake:
			n.runCycle(ctx, id)
		}
	}
}

func (n *Node) runCycle(ctx context.Context, repoID int64) {
	var err error
	if repoID != 0 {
		err = n.SyncRepo(ctx, repoID)
	} else {
		err = n.SyncOnce(ctx)
	}
	if err != nil && ctx.Err() == nil {
		n.log.Warn("sync cycle finished with errors", "err", err)
	}
}

// SyncOnce runs one full replication cycle against every peer and returns
// the joined errors (the cycle continues past individual failures).
func (n *Node) SyncOnce(ctx context.Context) error {
	n.syncMu.Lock()
	defer n.syncMu.Unlock()
	var errs []error
	if err := n.syncGlobal(ctx); err != nil {
		errs = append(errs, fmt.Errorf("global metadata from %s: %w", n.meta, err))
	}
	for _, peer := range n.peers {
		if err := n.syncPeer(ctx, peer, 0); err != nil {
			errs = append(errs, fmt.Errorf("peer %s: %w", peer, err))
			n.markPeerUnreachable(ctx, peer, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if n.opts.Metrics != nil {
		if led, err := n.opts.Store.CountLeaderRepos(ctx, n.opts.Name); err == nil {
			n.opts.Metrics.LeaderRepos.Set(float64(led))
		}
	}
	return errors.Join(errs...)
}

// SyncRepo synchronises one repository from its leader now (notify, resync).
func (n *Node) SyncRepo(ctx context.Context, repoID int64) error {
	n.syncMu.Lock()
	defer n.syncMu.Unlock()
	rp, err := n.opts.Store.RepoByID(ctx, repoID)
	if err != nil {
		// Unknown here yet: a full pass from every peer will pick it up.
		var errs []error
		for _, peer := range n.peers {
			if err := n.syncPeer(ctx, peer, repoID); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	if rp.LeaderNode == n.opts.Name {
		return ErrNotLeader
	}
	return n.syncPeer(ctx, rp.LeaderNode, repoID)
}

// syncGlobal pulls users, certificates and keys from the metadata leader.
func (n *Node) syncGlobal(ctx context.Context) error {
	if n.meta == n.opts.Name {
		return nil
	}
	var md store.TableRows
	if err := n.getJSON(ctx, n.meta, "/v1/users", &md); err != nil {
		return err
	}
	return n.opts.Store.ApplyGlobalMetadata(ctx, md)
}

// syncPeer pulls everything from one peer. When only is non-zero, only that
// repository's git and metadata are refreshed (events and records are still
// pulled, they are cheap and keep ordering simple).
func (n *Node) syncPeer(ctx context.Context, peer string, only int64) error {
	st := StatusResponse{}
	if err := n.getJSON(ctx, peer, "/v1/status", &st); err != nil {
		return err
	}
	addr, _ := n.PeerAddr(peer)
	if st.Node != peer {
		return fmt.Errorf("peer %s at %s identifies as %q", peer, addr, st.Node)
	}
	if err := n.opts.Store.UpsertNode(ctx, peer, addr, st.Version); err != nil {
		return err
	}
	// Repository records first so that events can reference them, then the
	// event log, then git and metadata of each repository the peer leads.
	repos, err := n.pullRepos(ctx, peer)
	if err != nil {
		return err
	}
	touched, err := n.pullEvents(ctx, peer)
	if err != nil {
		return err
	}
	if n.opts.Metrics != nil {
		cur, _ := n.opts.Store.ReplCursor(ctx, peer)
		n.opts.Metrics.ReplicaLag.WithLabelValues(peer).Set(float64(st.LastEventID - cur))
	}
	var errs []error
	for _, rp := range repos {
		if rp.LeaderNode != peer || !rp.DeletedAt.IsZero() {
			continue
		}
		if only != 0 && rp.ID != only {
			continue
		}
		if err := n.syncRepoFrom(ctx, peer, addr, rp, touched[rp.ID] || only == rp.ID); err != nil {
			errs = append(errs, fmt.Errorf("repo %s/%s: %w", rp.Owner, rp.Name, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

// pullRepos fetches the peer's repository records and upserts the ones it
// is authoritative for. Records that fail (for example an owner not yet
// replicated from the metadata leader) are logged and retried next cycle.
func (n *Node) pullRepos(ctx context.Context, peer string) ([]*store.Repo, error) {
	var wire []wireRepo
	if err := n.getJSON(ctx, peer, "/v1/repos", &wire); err != nil {
		return nil, err
	}
	var accepted []*store.Repo
	for _, w := range wire {
		rp := fromWireRepo(w)
		ok, err := n.acceptRepoRecord(ctx, peer, rp)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := n.opts.Store.UpsertRepo(ctx, rp); err != nil {
			n.log.Warn("repository record not applied", "peer", peer, "repo", rp.Owner+"/"+rp.Name, "err", err)
			continue
		}
		accepted = append(accepted, rp)
	}
	return accepted, nil
}

// acceptRepoRecord decides whether a repository record served by peer is
// authoritative for us: yes when the peer leads it, or when our own record
// names the peer as leader (so a leadership handoff announced by the old
// leader is honoured). Records for repositories we lead are never
// overwritten by a peer.
func (n *Node) acceptRepoRecord(ctx context.Context, peer string, rp *store.Repo) (bool, error) {
	local, err := n.opts.Store.RepoByID(ctx, rp.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return rp.LeaderNode == peer, nil
	case err != nil:
		return false, err
	case local.LeaderNode == n.opts.Name:
		return false, nil
	case local.LeaderNode == peer:
		return true, nil
	default:
		return rp.LeaderNode == peer, nil
	}
}

// pullEvents drains the peer's log from our cursor and returns the set of
// repository ids mentioned by new events.
func (n *Node) pullEvents(ctx context.Context, peer string) (map[int64]bool, error) {
	touched := map[int64]bool{}
	cursor, err := n.opts.Store.ReplCursor(ctx, peer)
	if err != nil {
		return nil, err
	}
	for {
		var batch []wireEvent
		if err := n.getJSON(ctx, peer, "/v1/events?after="+strconv.FormatInt(cursor, 10)+"&limit="+strconv.Itoa(eventBatch), &batch); err != nil {
			return touched, err
		}
		if len(batch) == 0 {
			return touched, nil
		}
		evs := make([]*store.Event, 0, len(batch))
		unknown := false
		for _, w := range batch {
			if w.ID <= cursor {
				return touched, fmt.Errorf("peer %s served event %d at or below cursor %d", peer, w.ID, cursor)
			}
			cursor = w.ID
			if w.RepoID != 0 && !touched[w.RepoID] {
				touched[w.RepoID] = true
				if _, err := n.opts.Store.RepoByID(ctx, w.RepoID); errors.Is(err, store.ErrNotFound) {
					unknown = true
				}
			}
			evs = append(evs, fromWireEvent(w))
		}
		if unknown {
			// A repository created after our record pull: refresh records so
			// the events keep their repository reference.
			if _, err := n.pullRepos(ctx, peer); err != nil {
				return touched, err
			}
		}
		if err := n.opts.Store.ApplyRemoteEvents(ctx, peer, evs); err != nil {
			return touched, err
		}
		if len(batch) < eventBatch {
			return touched, nil
		}
	}
}

// syncRepoFrom fetches git refs and metadata of one repository from its
// leader when the leader's timestamps moved, an event mentioned the repo, or
// the last attempt failed.
func (n *Node) syncRepoFrom(ctx context.Context, peer, addr string, rp *store.Repo, touched bool) error {
	prev, err := n.opts.Store.ReplicaFor(ctx, rp.ID, n.opts.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	updated, pushed := fmtTime(rp.UpdatedAt), fmtTime(rp.PushedAt)
	needGit := touched || prev == nil || prev.Status != store.ReplicaOK || prev.LeaderPushedAt != pushed
	needMeta := touched || prev == nil || prev.Status != store.ReplicaOK || prev.LeaderUpdatedAt != updated
	if !needGit && !needMeta {
		return nil
	}
	cursor, _ := n.opts.Store.ReplCursor(ctx, peer)
	rec := &store.Replica{RepoID: rp.ID, Node: n.opts.Name, LastEventID: cursor, LeaderUpdatedAt: updated, LeaderPushedAt: pushed}
	fail := func(err error) error {
		rec.Status, rec.Detail = store.ReplicaError, err.Error()
		if serr := n.opts.Store.SetReplica(ctx, rec); serr != nil {
			return errors.Join(err, serr)
		}
		return err
	}
	if needGit && rp.VCS == "git" {
		if err := n.fetchRepo(ctx, rp, addr); err != nil {
			return fail(fmt.Errorf("fetch: %w", err))
		}
	}
	if needMeta {
		var md store.TableRows
		if err := n.getJSON(ctx, peer, "/v1/repos/"+strconv.FormatInt(rp.ID, 10)+"/metadata", &md); err != nil {
			return fail(fmt.Errorf("metadata: %w", err))
		}
		if err := n.opts.Store.ApplyRepoMetadata(ctx, rp.ID, md); err != nil {
			return fail(fmt.Errorf("metadata: %w", err))
		}
	}
	rec.Status, rec.Detail = store.ReplicaOK, ""
	if err := n.opts.Store.SetReplica(ctx, rec); err != nil {
		return err
	}
	n.log.Debug("replica synced", "repo", rp.Owner+"/"+rp.Name, "leader", peer, "git", needGit, "metadata", needMeta)
	return nil
}

// markPeerUnreachable records a sync failure on every replica row of the
// repositories led by peer so that `pop status` and the lag metric show a
// dead leader instead of a frozen timestamp.
func (n *Node) markPeerUnreachable(ctx context.Context, peer string, cause error) {
	repos, err := n.opts.Store.AllRepos(ctx)
	if err != nil {
		return
	}
	for _, rp := range repos {
		if rp.LeaderNode != peer {
			continue
		}
		prev, _ := n.opts.Store.ReplicaFor(ctx, rp.ID, n.opts.Name)
		p := &store.Replica{RepoID: rp.ID, Node: n.opts.Name, Status: store.ReplicaError, Detail: "peer unreachable: " + cause.Error()}
		if prev != nil {
			p.LastEventID = prev.LastEventID
		}
		_ = n.opts.Store.SetReplica(ctx, p)
	}
}
