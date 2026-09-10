package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"as215520.net/forge/internal/store"
)

// Operator procedures behind `forge admin repl ...`. Status needs only the
// store; Resync and MoveLeader need a Node (peers and secret) but not a
// running server, so the CLI can construct one with repl.New and call them
// directly against the shared database.

// ReplicaStatus is one row of `forge admin repl status`.
type ReplicaStatus struct {
	RepoID       int64
	Repo         string // owner/name
	LeaderNode   string
	Node         string
	Status       string
	Detail       string
	LastSyncedAt time.Time
	LastEventID  int64
	// LeaderCursor is our cursor into the leader's event log (0 if unknown).
	LeaderCursor int64
}

// Status lists replica rows recorded in this node's database together with
// the per-origin cursors.
func Status(ctx context.Context, st *store.Store) ([]ReplicaStatus, error) {
	reps, err := st.Replicas(ctx)
	if err != nil {
		return nil, err
	}
	cursors, err := st.ReplCursors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ReplicaStatus, 0, len(reps))
	for _, p := range reps {
		out = append(out, ReplicaStatus{
			RepoID: p.RepoID, Repo: p.Owner + "/" + p.RepoName, LeaderNode: p.LeaderNode, Node: p.Node,
			Status: p.Status, Detail: p.Detail, LastSyncedAt: p.LastSyncedAt, LastEventID: p.LastEventID,
			LeaderCursor: cursors[p.LeaderNode],
		})
	}
	return out, nil
}

// Resync forces a full fetch and metadata pull of one repository from its
// leader, regardless of what the last sync recorded.
func (n *Node) Resync(ctx context.Context, repoID int64) error {
	rp, err := n.opts.Store.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if rp.LeaderNode == n.opts.Name {
		return ErrNotLeader
	}
	if err := n.opts.Store.SetReplica(ctx, &store.Replica{RepoID: rp.ID, Node: n.opts.Name, Status: store.ReplicaResync, Detail: "resync requested"}); err != nil {
		return err
	}
	return n.SyncRepo(ctx, repoID)
}

// MoveLeader transfers leadership of a repository from this node to newNode.
// It must run on the current leader: repository records replicate from
// their leader, so only the leader's change of `leader_node` reaches every
// replica (see docs/replication.md). It refuses unless the target holds the
// same refs and has applied the leader's latest metadata; the caller should
// stop writes (drain) first, as a push racing this check would be lost.
func (n *Node) MoveLeader(ctx context.Context, repoID int64, newNode string) error {
	if newNode == n.opts.Name {
		return errors.New("repl: already the leader")
	}
	if _, err := n.PeerAddr(newNode); err != nil {
		return err
	}
	rp, err := n.opts.Store.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if rp.LeaderNode != n.opts.Name {
		return fmt.Errorf("%w: run move-leader on %s", ErrNotLeader, rp.LeaderNode)
	}
	if rp.VCS == "git" {
		local, err := n.opts.Git.Open(n.RepoPath(rp.Owner, rp.Name))
		if err != nil {
			return err
		}
		refs, err := local.Refs(ctx)
		if err != nil {
			return err
		}
		var remote RepoState
		if err := n.getJSON(ctx, newNode, "/v1/repos/"+strconv.FormatInt(rp.ID, 10)+"/state", &remote); err != nil {
			return err
		}
		if !refsEqual(refs, remote.Refs) {
			return fmt.Errorf("%w: refs on %s differ from the leader", ErrNotSynced, newNode)
		}
		if remote.ReplicaStatus != store.ReplicaOK || remote.LeaderUpdatedAt != fmtTime(rp.UpdatedAt) {
			return fmt.Errorf("%w: %s has replica status %q for metadata version %q (leader has %q)", ErrNotSynced, newNode, remote.ReplicaStatus, remote.LeaderUpdatedAt, fmtTime(rp.UpdatedAt))
		}
	}
	if err := n.opts.Store.SetRepoLeader(ctx, rp.ID, newNode); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"from": n.opts.Name, "to": newNode})
	if _, err := n.opts.Store.AddEvent(ctx, &store.Event{Kind: store.EventAdminAction, RepoID: rp.ID,
		Subject: "leadership of " + rp.Owner + "/" + rp.Name + " moved to " + newNode, Path: "/" + rp.Owner + "/" + rp.Name, Payload: payload}); err != nil {
		return err
	}
	n.log.Info("leadership moved", "repo", rp.Owner+"/"+rp.Name, "from", n.opts.Name, "to", newNode)
	n.NotifyPeers(ctx, rp.ID)
	return nil
}
