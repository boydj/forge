package web

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/vcs"
	"as215520.net/forge/internal/version"
)

// The status page (/status/) is the fleet as this node sees it: every point
// of presence with its health verdict, announcement state and replication
// lag, an overall banner derived from them, and the most recent incident
// reports. Nodes come from the replication control plane (FleetSource); a
// single node without a cluster shows itself. Incidents are files in the
// documentation repository, so writing one is a git push, and the list is
// also served as a gemfeed and an Atom feed to subscribe to.

// FleetNode is one node as the status page sees it.
type FleetNode struct {
	Node    string
	Version string
	// Self marks the node rendering the page.
	Self bool
	// Reachable is whether the last control-plane poll succeeded (always
	// true for Self). Err holds the last error otherwise.
	Reachable bool
	Err       string
	// SeenAt is the last successful poll (zero: never).
	SeenAt time.Time
	// Healthy is the health controller's verdict; nil when the node runs
	// none (then it counts as healthy). Detail lists failing checks.
	Healthy *bool
	Detail  string
	// State is the announcement state: announced, drained, withdrawn, or ""
	// when the node has no announcer (a single node serves regardless).
	State  string
	Manual bool
	Checks []FleetCheck
	// StartedAt is the process start (zero: unknown).
	StartedAt time.Time
	// ReplicaLag is events behind the furthest leader; -1 unknown.
	ReplicaLag  int64
	LeaderRepos int
}

// FleetCheck is one health check on one node.
type FleetCheck struct {
	Name   string
	OK     bool
	Detail string
}

// FleetSource lists every node of the cluster, this one included.
type FleetSource interface {
	Fleet(ctx context.Context) []FleetNode
}

// healthy is the node's verdict, treating "no controller" as healthy.
func (n FleetNode) healthy() bool { return n.Healthy == nil || *n.Healthy }

// serving is whether the node attracts and answers traffic: reachable,
// healthy and announced (or with no announcer at all).
func (n FleetNode) serving() bool {
	return n.Reachable && n.healthy() && (n.State == "announced" || n.State == "")
}

// components maps health check names to what users know them as, in
// display order.
var components = []struct{ check, label string }{
	{"gemini", "Gemini and Titan"},
	{"ssh", "Git over SSH"},
	{"db", "Metadata database"},
	{"disk", "Storage"},
	{"replica", "Replication"},
}

func (h *Handler) statusRoutes(req *request, rest []string, trailing bool) {
	switch {
	case len(rest) == 0 && !trailing:
		h.status(req)
	case len(rest) == 0:
		h.statusPage(req)
	case len(rest) == 1 && rest[0] == "feed":
		h.incidentFeed(req, false)
	case len(rest) == 1 && rest[0] == "atom.xml":
		h.incidentFeed(req, true)
	default:
		_ = gemini.NotFound(req.w)
	}
}

// selfNode describes this node without a cluster.
func (h *Handler) selfNode() FleetNode {
	n := FleetNode{Node: h.F.Config.Node, Version: version.Version, Self: true, Reachable: true, SeenAt: time.Now(), StartedAt: h.Started, ReplicaLag: -1}
	if h.Health != nil {
		ok, detail := h.Health()
		n.Healthy, n.Detail = &ok, detail
	}
	return n
}

func (h *Handler) fleet(ctx context.Context) []FleetNode {
	var nodes []FleetNode
	if h.Fleet != nil {
		nodes = h.Fleet.Fleet(ctx)
	}
	if len(nodes) == 0 {
		nodes = []FleetNode{h.selfNode()}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	return nodes
}

func (h *Handler) statusPage(req *request) {
	nodes := h.fleet(req.ctx)
	total, reachable, serving, announcing, withState := len(nodes), 0, 0, 0, 0
	for _, n := range nodes {
		if n.Reachable {
			reachable++
		}
		if n.serving() {
			serving++
		}
		if n.State != "" {
			withState++
			if n.Reachable && n.State == "announced" {
				announcing++
			}
		}
	}
	p := req.page(h.F.Config.Title + " status")
	switch {
	case serving == total:
		p.Heading(2, "All systems operational")
	case serving > 0:
		p.Heading(2, fmt.Sprintf("Degraded: %d of %d points of presence serving", serving, total))
	default:
		p.Heading(2, "Major outage: no point of presence is serving")
	}
	p.Blank()

	p.Heading(2, "Components")
	for _, c := range components {
		ok, seen := 0, 0
		for _, n := range nodes {
			if !n.Reachable {
				continue
			}
			for _, chk := range n.Checks {
				if chk.Name != c.check {
					continue
				}
				seen++
				if chk.OK {
					ok++
				}
			}
		}
		var verdict string
		switch {
		case seen == 0:
			verdict = "no data"
		case ok == seen:
			verdict = "operational"
		case ok > 0:
			verdict = fmt.Sprintf("degraded (%d of %d)", ok, seen)
		default:
			verdict = "down"
		}
		p.Text(c.label + ": " + verdict)
	}
	switch {
	case withState == 0:
		p.Text("Anycast: not configured")
	case announcing == 0:
		p.Text(fmt.Sprintf("Anycast: no point of presence announcing (%d configured)", withState))
	default:
		p.Text(fmt.Sprintf("Anycast: %d of %d announcing", announcing, withState))
	}
	if total > 1 {
		p.Text(fmt.Sprintf("Control plane: %d of %d nodes reachable", reachable, total))
	}
	p.Blank()

	p.Heading(2, "Points of presence")
	for _, n := range nodes {
		p.Text(nodeLine(n, time.Now()))
	}
	p.Blank()

	p.Heading(2, "Recent incidents")
	incidents := h.incidents(req.ctx, 10)
	if len(incidents) == 0 {
		p.Text("No incidents recorded.")
	}
	for _, in := range incidents {
		p.Link(in.Path, in.Date+" - "+in.Title)
	}
	p.Link("/status/feed", "incident feed")
	p.Link("/status/atom.xml", "incident feed (Atom)")
	p.Blank()
	p.Text(fmt.Sprintf("Generated %s by %s.", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"), h.F.Config.Node))
	p.Link("/docs/", "documentation")
	h.footer(p, req)
	req.send(p)
}

// nodeLine formats one node for the points-of-presence list.
func nodeLine(n FleetNode, now time.Time) string {
	var parts []string
	switch {
	case !n.Reachable && n.SeenAt.IsZero():
		parts = append(parts, "unreachable (never seen)")
	case !n.Reachable:
		parts = append(parts, "unreachable since "+n.SeenAt.UTC().Format("2006-01-02 15:04 UTC"))
	default:
		if n.State != "" {
			s := n.State
			if n.Manual {
				s += " by operator"
			}
			parts = append(parts, s)
		}
		if n.healthy() {
			parts = append(parts, "healthy")
		} else if n.Detail != "" {
			parts = append(parts, "unhealthy ("+n.Detail+")")
		} else {
			parts = append(parts, "unhealthy")
		}
		if n.ReplicaLag >= 0 {
			parts = append(parts, fmt.Sprintf("lag %d", n.ReplicaLag))
		}
		if n.Version != "" {
			parts = append(parts, "v"+strings.TrimPrefix(n.Version, "v"))
		}
		if !n.StartedAt.IsZero() {
			parts = append(parts, "up "+brief(now.Sub(n.StartedAt)))
		}
		if !n.Self && !n.SeenAt.IsZero() {
			parts = append(parts, "seen "+brief(now.Sub(n.SeenAt))+" ago")
		}
	}
	line := n.Node + ": " + strings.Join(parts, ", ")
	if n.Self {
		line += " (this node)"
	}
	if !n.Reachable && n.Err != "" {
		line += " - " + n.Err
	}
	return line
}

// brief renders a duration as its two most significant units.
func brief(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// Incidents.

type incident struct {
	Date  string // YYYY-MM-DD
	Title string
	Path  string // /docs/... URL
}

var incidentName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-([^/]+)\.(md|markdown|gmi|gemini)$`)

// incidents lists the newest incident reports from the documentation
// repository (config docs.incidents), newest first, at most limit.
func (h *Handler) incidents(ctx context.Context, limit int) []incident {
	cfg := h.F.Config.Docs
	owner, name, ok := strings.Cut(cfg.Repo, "/")
	if cfg.Repo == "" || !ok || cfg.Incidents == "" {
		return nil
	}
	acc, err := h.F.LookupRepo(ctx, nil, owner, name)
	if err != nil {
		return nil
	}
	repo, err := h.F.Open(acc.Repo)
	if err != nil {
		return nil
	}
	ref := cfg.Ref
	if ref == "" {
		ref = acc.Repo.DefaultBranch
	}
	id, err := repo.Resolve(ctx, ref)
	if err != nil {
		return nil
	}
	dir := gitPath(cfg.Path, cfg.Incidents)
	entries, err := repo.Tree(ctx, id, dir)
	if err != nil {
		return nil
	}
	var files []vcs.TreeEntry
	for _, e := range entries {
		if (e.Kind == vcs.EntryFile || e.Kind == vcs.EntryExecutable) && incidentName.MatchString(e.Name) {
			files = append(files, e)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })
	if len(files) > limit {
		files = files[:limit]
	}
	var out []incident
	for _, e := range files {
		m := incidentName.FindStringSubmatch(e.Name)
		title := h.incidentTitle(ctx, repo, id, gitPath(dir, e.Name), e.ID)
		if title == "" {
			title = strings.ReplaceAll(m[2], "-", " ")
		}
		out = append(out, incident{Date: m[1], Title: title, Path: docsBase(cfg.Incidents) + e.Name})
	}
	return out
}

// incidentTitle is the first heading of a report, cached by blob id
// (content-addressed, so never stale).
func (h *Handler) incidentTitle(ctx context.Context, repo vcs.Repository, id vcs.RevisionID, full, blobID string) string {
	h.titleMu.Lock()
	if t, ok := h.titles[blobID]; ok {
		h.titleMu.Unlock()
		return t
	}
	h.titleMu.Unlock()
	blob, err := repo.Blob(ctx, id, full)
	if err != nil {
		return ""
	}
	defer blob.Close()
	data, err := io.ReadAll(io.LimitReader(blob.Reader, 4096))
	if err != nil || !utf8.Valid(data) {
		return ""
	}
	title := ""
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			title = strings.TrimSpace(strings.TrimLeft(l, "#"))
			break
		}
	}
	h.titleMu.Lock()
	if h.titles == nil || len(h.titles) > 4096 {
		h.titles = map[string]string{}
	}
	h.titles[blobID] = title
	h.titleMu.Unlock()
	return title
}

// incidentFeed serves the incident list as a gemfeed or an Atom feed.
func (h *Handler) incidentFeed(req *request, atom bool) {
	incidents := h.incidents(req.ctx, feedLimit)
	title := h.F.Config.Title + " incidents"
	if !atom {
		p := req.page(title)
		p.Heading(2, "Gemfeed from "+h.F.Config.Hostname)
		p.Link("/status/", "status")
		p.Blank()
		for _, in := range incidents {
			p.Link(in.Path, in.Date+" - "+in.Title)
		}
		if len(incidents) == 0 {
			p.Text("No incidents recorded.")
		}
		req.send(p)
		return
	}
	home := h.F.Config.GeminiURL("/status/")
	f := atomFeed{NS: "http://www.w3.org/2005/Atom", Title: title, ID: home, Link: atomLink{Href: home}}
	f.Updated = time.Now().UTC().Format(time.RFC3339)
	for i, in := range incidents {
		updated := in.Date + "T00:00:00Z"
		if i == 0 {
			f.Updated = updated
		}
		f.Entries = append(f.Entries, atomEntry{Title: in.Title, ID: h.F.Config.GeminiURL(in.Path), Updated: updated, Link: atomLink{Href: h.F.Config.GeminiURL(in.Path)}})
	}
	_ = req.w.Header(gemini.StatusSuccess, "application/atom+xml; charset=utf-8")
	_, _ = req.w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(req.w)
	enc.Indent("", "  ")
	_ = enc.Encode(f)
	_, _ = req.w.Write([]byte("\n"))
}

// titleCache is embedded in Handler.
type titleCache struct {
	titleMu sync.Mutex
	titles  map[string]string
}
