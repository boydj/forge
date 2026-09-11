package main

import (
	"context"
	"time"

	"as215520.net/forge/internal/repl"
	"as215520.net/forge/internal/web"
)

// fleetSource adapts the replication node's view of its peers to the status
// page. It lives here so that internal/repl and internal/web stay
// independent of each other.
type fleetSource struct {
	rn *repl.Node
}

func (f fleetSource) Fleet(ctx context.Context) []web.FleetNode {
	now := time.Now()
	var out []web.FleetNode
	if st, err := f.rn.SelfStatus(ctx); err == nil {
		self := fleetNode(st, now, "")
		self.Self = true
		out = append(out, self)
	} else {
		out = append(out, web.FleetNode{Node: f.rn.Name(), Self: true, Reachable: false, Err: err.Error(), ReplicaLag: -1})
	}
	for name, ps := range f.rn.Fleet() {
		n := fleetNode(ps.Status, ps.SeenAt, ps.Err)
		if n.Node == "" {
			n.Node = name
		}
		if !ps.Reachable() {
			n.Reachable = false
		}
		out = append(out, n)
	}
	return out
}

func fleetNode(st repl.StatusResponse, seen time.Time, errStr string) web.FleetNode {
	n := web.FleetNode{Node: st.Node, Version: st.Version, Reachable: errStr == "", Err: errStr, SeenAt: seen, ReplicaLag: st.ReplicaLag, LeaderRepos: st.LeaderRepos}
	if st.StartedAt != "" {
		if t, err := time.Parse(time.RFC3339, st.StartedAt); err == nil {
			n.StartedAt = t
		}
	}
	if st.Health != nil {
		healthy := st.Health.Healthy
		n.Healthy, n.Detail, n.State, n.Manual = &healthy, st.Health.Detail, st.Health.State, st.Health.Manual
		for _, c := range st.Health.Checks {
			n.Checks = append(n.Checks, web.FleetCheck{Name: c.Name, OK: c.OK, Detail: c.Detail})
		}
	}
	return n
}
