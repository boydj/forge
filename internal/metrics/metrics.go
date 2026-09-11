// Package metrics exposes Prometheus metrics for the daemon. The registry
// is served over plain HTTP on a private address only.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds all forge metrics.
type Registry struct {
	reg *prometheus.Registry

	geminiRequests *prometheus.CounterVec
	geminiDuration *prometheus.HistogramVec
	geminiBody     prometheus.Counter
	geminiConns    prometheus.Gauge

	sshSessions *prometheus.CounterVec
	sshDuration *prometheus.HistogramVec
	sshConns    prometheus.Gauge
	sshForwards *prometheus.CounterVec

	RepoCount     prometheus.Gauge
	RepoBytes     prometheus.Gauge
	UserCount     prometheus.Gauge
	DiskFree      prometheus.Gauge
	ReplicaLag    *prometheus.GaugeVec
	LeaderRepos   prometheus.Gauge
	Healthy       prometheus.Gauge
	Announced     prometheus.Gauge
	BackupAge     prometheus.Gauge
	HookDecisions *prometheus.CounterVec
}

// New builds a registry with process/go collectors.
func New() *Registry {
	r := &Registry{reg: prometheus.NewRegistry()}
	r.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := promauto(r.reg)
	r.geminiRequests = f.counterVec("forge_gemini_requests_total", "Gemini/Titan requests by scheme and status.", "scheme", "status")
	r.geminiDuration = f.histVec("forge_gemini_request_seconds", "Request duration.", "scheme")
	r.geminiBody = f.counter("forge_titan_body_bytes_total", "Titan body bytes received.")
	r.geminiConns = f.gauge("forge_gemini_connections", "Open Gemini/Titan connections.")
	r.sshSessions = f.counterVec("forge_ssh_sessions_total", "SSH git sessions by operation and result.", "op", "result")
	r.sshDuration = f.histVec("forge_ssh_session_seconds", "SSH session duration.", "op")
	r.sshConns = f.gauge("forge_ssh_connections", "Open SSH connections.")
	r.sshForwards = f.counterVec("forge_ssh_push_forwards_total", "Pushes relayed between replica and leader, by this node's role and result.", "role", "result")
	r.RepoCount = f.gauge("forge_repositories", "Number of repositories.")
	r.RepoBytes = f.gauge("forge_repository_bytes", "Total repository bytes.")
	r.UserCount = f.gauge("forge_users", "Number of accounts.")
	r.DiskFree = f.gauge("forge_disk_free_bytes", "Free bytes on the data filesystem.")
	r.ReplicaLag = f.gaugeVec("forge_replica_lag_events", "Events behind the leader, per repository leader node.", "leader")
	r.LeaderRepos = f.gauge("forge_leader_repositories", "Repositories led by this node.")
	r.Healthy = f.gauge("forge_healthy", "1 when the node passes health checks.")
	r.Announced = f.gauge("forge_bgp_announced", "1 when anycast prefixes are announced by this node.")
	r.BackupAge = f.gauge("forge_backup_age_seconds", "Age of the newest successful backup.")
	r.HookDecisions = f.counterVec("forge_hook_decisions_total", "Push hook decisions.", "hook", "decision")
	return r
}

// Handler serves /metrics.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{}))
	return mux
}

// Gemini returns the adapter used by the Gemini server.
func (r *Registry) Gemini() *GeminiMetrics { return &GeminiMetrics{r: r} }

// GeminiMetrics implements gemini.Metrics.
type GeminiMetrics struct{ r *Registry }

// ObserveRequest records one request.
func (g *GeminiMetrics) ObserveRequest(scheme string, status int, d time.Duration, body int64) {
	g.r.geminiRequests.WithLabelValues(scheme, strconv.Itoa(status/10*10)).Inc()
	g.r.geminiDuration.WithLabelValues(scheme).Observe(d.Seconds())
	if body > 0 {
		g.r.geminiBody.Add(float64(body))
	}
}

// ConnectionsChanged adjusts the open-connection gauge.
func (g *GeminiMetrics) ConnectionsChanged(delta int) { g.r.geminiConns.Add(float64(delta)) }

// SSH returns the adapter used by the SSH server.
func (r *Registry) SSH() *SSHMetrics { return &SSHMetrics{r: r} }

// SSHMetrics implements sshd.Metrics.
type SSHMetrics struct{ r *Registry }

// ObserveSession records one git session.
func (s *SSHMetrics) ObserveSession(op string, success bool, d time.Duration) {
	res := "ok"
	if !success {
		res = "error"
	}
	s.r.sshSessions.WithLabelValues(op, res).Inc()
	s.r.sshDuration.WithLabelValues(op).Observe(d.Seconds())
}

// ConnectionsChanged adjusts the open-connection gauge.
func (s *SSHMetrics) ConnectionsChanged(delta int) { s.r.sshConns.Add(float64(delta)) }

// ObserveForward records one forwarded push (sshd.ForwardMetrics): role is
// "replica" or "leader", result "ok", "rejected" or "unreachable".
func (s *SSHMetrics) ObserveForward(role, result string) {
	s.r.sshForwards.WithLabelValues(role, result).Inc()
}

type factory struct{ reg *prometheus.Registry }

func promauto(reg *prometheus.Registry) factory { return factory{reg: reg} }

func (f factory) counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	f.reg.MustRegister(c)
	return c
}

func (f factory) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.reg.MustRegister(g)
	return g
}

func (f factory) gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(g)
	return g
}

func (f factory) histVec(name, help string, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}}, labels)
	f.reg.MustRegister(h)
	return h
}
