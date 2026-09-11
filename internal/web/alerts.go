package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"as215520.net/forge/internal/gemini"
)

// Alerts feed. Prometheus on the monitoring host evaluates the alert rules;
// every forge node reads its firing alerts over the control network and
// serves them as a gemfeed (/status/alerts, Atom twin at
// /status/alerts/atom.xml) so the operator subscribes to alerts the way they
// subscribe to a repository. No Alertmanager, no push channel, no state: an
// alert is an entry while it fires and disappears when it resolves (feed
// readers keep what they saw). Each entry links to /status/alerts/<id>.

const (
	alertsTTL     = 30 * time.Second
	alertsTimeout = 5 * time.Second
)

// Alert is one firing alert as Prometheus reports it.
type Alert struct {
	ID          string // stable per alert: hash of its labels
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	ActiveAt    time.Time
}

// pop is the point of presence the alert concerns, if any.
func (a Alert) pop() string {
	if p := a.Labels["pop"]; p != "" {
		return p
	}
	return a.Labels["node"]
}

// title is the feed entry title: severity, name, place and summary.
func (a Alert) title() string {
	t := "FIRING " + a.Name
	if p := a.pop(); p != "" {
		t += " on " + p
	}
	if s := a.Annotations["summary"]; s != "" {
		t += ": " + s
	}
	return t
}

// alertCache holds the last successful read for alertsTTL.
type alertCache struct {
	mu      sync.Mutex
	fetched time.Time
	alerts  []Alert
	err     error
}

// alerts returns the firing alerts, from cache when fresh. A source error
// is returned (and logged) but the last good list is kept for the status
// page's count.
func (h *Handler) alerts(ctx context.Context) ([]Alert, error) {
	url := h.F.Config.Status.PrometheusURL
	if url == "" {
		return nil, errors.New("no alert source configured")
	}
	c := &h.alertCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetched) < alertsTTL {
		return c.alerts, c.err
	}
	alerts, err := fetchAlerts(ctx, url)
	c.fetched = time.Now()
	if err != nil {
		h.Log.Warn("alert source", "url", url, "err", err)
		c.err = err
		return c.alerts, err
	}
	c.alerts, c.err = alerts, nil
	return alerts, nil
}

var alertClient = &http.Client{Timeout: alertsTimeout}

// fetchAlerts reads GET <base>/api/v1/alerts and keeps the firing ones,
// newest first.
func fetchAlerts(ctx context.Context, base string) ([]Alert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/alerts", nil)
	if err != nil {
		return nil, err
	}
	resp, err := alertClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %s", resp.Status)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    time.Time         `json:"activeAt"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: status %q", body.Status)
	}
	var out []Alert
	for _, a := range body.Data.Alerts {
		if a.State != "firing" {
			continue
		}
		out = append(out, Alert{ID: alertID(a.Labels), Name: a.Labels["alertname"], Labels: a.Labels, Annotations: a.Annotations, ActiveAt: a.ActiveAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ActiveAt.Equal(out[j].ActiveAt) {
			return out[i].ActiveAt.After(out[j].ActiveAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// alertID hashes the sorted label set: the same alert keeps the same id
// (and feed entry) for as long as it fires.
func alertID(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(labels[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// alertFeed serves the firing alerts as a gemfeed or an Atom feed. Without a
// configured source, or with the source unreachable, the page still answers
// 20 (a valid, empty feed) and says why.
func (h *Handler) alertFeed(req *request, atom bool) {
	title := h.F.Config.Title + " alerts"
	var alerts []Alert
	note := ""
	if h.F.Config.Status.PrometheusURL == "" {
		note = "Alerts are not configured on this node."
	} else if a, err := h.alerts(req.ctx); err != nil {
		note = "Alert source unreachable; showing the last known state."
		alerts = a
	} else {
		alerts = a
	}
	if !atom {
		p := req.page(title)
		p.Heading(2, "Gemfeed from "+h.F.Config.Hostname)
		p.Link("/status/", "status")
		p.Blank()
		if note != "" {
			p.Text(note)
		}
		for _, a := range alerts {
			p.Link("/status/alerts/"+a.ID, a.ActiveAt.UTC().Format("2006-01-02")+" - "+a.title())
		}
		if len(alerts) == 0 && note == "" {
			p.Text("No alerts firing.")
		}
		req.send(p)
		return
	}
	home := h.F.Config.GeminiURL("/status/alerts")
	f := atomFeed{NS: "http://www.w3.org/2005/Atom", Title: title, ID: home, Link: atomLink{Href: home}}
	f.Updated = time.Now().UTC().Format(time.RFC3339)
	for i, a := range alerts {
		u := h.F.Config.GeminiURL("/status/alerts/" + a.ID)
		if i == 0 {
			f.Updated = a.ActiveAt.UTC().Format(time.RFC3339)
		}
		f.Entries = append(f.Entries, atomEntry{Title: a.title(), ID: u, Updated: a.ActiveAt.UTC().Format(time.RFC3339), Link: atomLink{Href: u}, Summary: a.Annotations["description"]})
	}
	_ = req.w.Header(gemini.StatusSuccess, "application/atom+xml; charset=utf-8")
	_, _ = req.w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(req.w)
	enc.Indent("", "  ")
	_ = enc.Encode(f)
	_, _ = req.w.Write([]byte("\n"))
}

// alertPage shows one alert while it fires; afterwards it says so, so a
// feed reader's stale entry still lands somewhere sensible.
func (h *Handler) alertPage(req *request, id string) {
	if h.F.Config.Status.PrometheusURL == "" {
		_ = gemini.NotFound(req.w)
		return
	}
	alerts, _ := h.alerts(req.ctx)
	for _, a := range alerts {
		if a.ID != id {
			continue
		}
		p := req.page(a.title())
		p.Text("Firing since " + a.ActiveAt.UTC().Format("2006-01-02 15:04 UTC") + " (" + brief(time.Since(a.ActiveAt)) + ").")
		if s := a.Labels["severity"]; s != "" {
			p.Text("Severity: " + s)
		}
		if d := a.Annotations["description"]; d != "" {
			p.Blank()
			p.Text(d)
		}
		if r := a.Annotations["runbook"]; r != "" {
			p.Text("Runbook: " + r)
		}
		p.Blank()
		p.Heading(2, "Labels")
		keys := make([]string, 0, len(a.Labels))
		for k := range a.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p.Item(k + " = " + a.Labels[k])
		}
		p.Blank()
		p.Link("/status/alerts", "alerts feed")
		p.Link("/status/", "status")
		h.footer(p, req)
		req.send(p)
		return
	}
	p := req.page("Alert " + id)
	p.Text("This alert is no longer firing (or never was).")
	p.Link("/status/alerts", "alerts feed")
	p.Link("/status/", "status")
	h.footer(p, req)
	req.send(p)
}
