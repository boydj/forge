package repl

import (
	"context"
	"fmt"
	"sync"
	"time"

	"as215520.net/forge/internal/health"
)

// Fleet view: every node polls every peer's /v1/status so that any node can
// render the whole cluster on its status page without depending on the
// monitoring host. Polls are cheap (one small JSON document per peer) and
// independent of replication, so a replica that has fallen behind still
// reports its peers correctly.

// fleetInterval is how often peers are polled.
const fleetInterval = 30 * time.Second

// fleetTimeout bounds one status poll.
const fleetTimeout = 10 * time.Second

// PeerStatus is the last known control-plane status of one peer.
type PeerStatus struct {
	// Status is the last successful response (zero value before any).
	Status StatusResponse
	// SeenAt is the time of the last successful poll (zero: never).
	SeenAt time.Time
	// Checked is the time of the last attempt.
	Checked time.Time
	// Err is the last error; "" when the last poll succeeded.
	Err string
}

// Reachable reports whether the last poll succeeded.
func (p PeerStatus) Reachable() bool { return p.Err == "" && !p.SeenAt.IsZero() }

func (n *Node) fleetLoop(ctx context.Context) {
	n.PollFleet(ctx)
	t := time.NewTicker(fleetInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.PollFleet(ctx)
		}
	}
}

// PollFleet fetches every peer's status once, in parallel, and records the
// outcome. Run calls it periodically; tests and admin tooling call it
// directly.
func (n *Node) PollFleet(ctx context.Context) {
	var wg sync.WaitGroup
	for _, peer := range n.peers {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, fleetTimeout)
			defer cancel()
			var st StatusResponse
			err := n.getJSON(cctx, peer, "/v1/status", &st)
			if err == nil && st.Node != peer {
				addr, _ := n.PeerAddr(peer)
				err = fmt.Errorf("peer %s at %s identifies as %q", peer, addr, st.Node)
			}
			n.fleetMu.Lock()
			defer n.fleetMu.Unlock()
			ps := n.fleet[peer]
			ps.Checked = time.Now()
			if err != nil {
				ps.Err = err.Error()
			} else {
				ps.Err = ""
				ps.Status = st
				ps.SeenAt = ps.Checked
			}
			n.fleet[peer] = ps
		}(peer)
	}
	wg.Wait()
}

// Fleet returns a copy of the last known status of every peer, keyed by
// peer name. Peers never polled successfully have a zero Status.
func (n *Node) Fleet() map[string]PeerStatus {
	n.fleetMu.RLock()
	defer n.fleetMu.RUnlock()
	out := make(map[string]PeerStatus, len(n.peers))
	for _, peer := range n.peers {
		out[peer] = n.fleet[peer]
	}
	return out
}

// SelfStatus is this node's own /v1/status document.
func (n *Node) SelfStatus(ctx context.Context) (StatusResponse, error) { return n.status(ctx) }

func (n *Node) setLag(peer string, events int64) {
	n.lagMu.Lock()
	defer n.lagMu.Unlock()
	n.lag[peer] = events
}

// maxLag is the largest recorded lag behind any leader, -1 before any sync.
func (n *Node) maxLag() int64 {
	n.lagMu.Lock()
	defer n.lagMu.Unlock()
	if len(n.lag) == 0 {
		return -1
	}
	var m int64
	for _, v := range n.lag {
		if v > m {
			m = v
		}
	}
	return m
}

// healthStatus converts a controller snapshot to its wire form.
func healthStatus(s health.Status) *HealthStatus {
	hs := &HealthStatus{Healthy: s.Healthy, Detail: s.Detail, State: s.State.String(), Manual: s.Manual}
	for _, r := range s.Results {
		c := CheckStatus{Name: r.Name, OK: r.OK()}
		if r.Err != nil {
			c.Detail = r.Err.Error()
		}
		hs.Checks = append(hs.Checks, c)
	}
	return hs
}
