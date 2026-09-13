// Package mirror pushes repositories to external git remotes (a GitHub
// mirror, docs/dogfooding.md "Mirror strategy"). Only the repository's
// leader mirrors: after every successful push it schedules `git push
// --mirror <url>` (debounced, so a burst of pushes is one mirror push) and a
// periodic reconcile pushes every configured mirror regardless. Failures
// are retried with backoff and never block a push. The remote is reached
// over ssh with a dedicated deploy key and pinned host keys, or over https.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

// ErrNotLeader means this node does not lead the repository; the leader
// mirrors it.
var ErrNotLeader = errors.New("mirror: not the repository's leader")

// ErrNoMirror means the repository has no configured mirror.
var ErrNoMirror = errors.New("mirror: no mirror configured for this repository")

// Options configure the worker.
type Options struct {
	// Targets maps "owner/name" to the remote URL.
	Targets map[string]string
	// KeyFile and KnownHostsFile are used for ssh remotes.
	KeyFile        string
	KnownHostsFile string
	// Interval is the periodic reconcile (default 1h).
	Interval time.Duration
	// Timeout bounds one mirror push (default 10m).
	Timeout time.Duration
	// Backoff are the retry delays after consecutive failures; the last one
	// repeats (default 1m, 5m, 30m).
	Backoff []time.Duration
	// Debounce delays a push-triggered mirror so a burst coalesces (default 2s).
	Debounce time.Duration
	// ReposDir holds <owner>/<name>.git.
	ReposDir string
	Git      *gitvcs.Backend
	// Store persists the last success time (settings) so the metric survives
	// restarts. Optional.
	Store *store.Store
	// Leads reports whether this node leads the repository. nil: always.
	Leads   func(ctx context.Context, repo string) (bool, error)
	Log     *slog.Logger
	Metrics *metrics.Registry
	Now     func() time.Time
}

type entry struct {
	due      time.Time
	attempts int
}

// Worker runs mirror pushes.
type Worker struct {
	o       Options
	enabled bool
	reason  string

	mu    sync.Mutex
	queue map[string]*entry
	wake  chan struct{}
}

// New builds a worker. It is disabled (Enabled() false, with the reason
// logged once) when nothing is configured or an ssh remote is configured
// without a usable key file.
func New(o Options) *Worker {
	if o.Interval <= 0 {
		o.Interval = time.Hour
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
	}
	if len(o.Backoff) == 0 {
		o.Backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}
	}
	if o.Debounce <= 0 {
		o.Debounce = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.Log = o.Log.With("component", "mirror")
	w := &Worker{o: o, queue: map[string]*entry{}, wake: make(chan struct{}, 1)}
	if len(o.Targets) == 0 {
		w.reason = "no mirrors configured"
		return w
	}
	needKey := false
	for _, u := range o.Targets {
		if isSSH(u) {
			needKey = true
		}
	}
	if needKey {
		if o.KeyFile == "" {
			w.reason = "ssh mirrors configured but no key file"
			o.Log.Warn("mirroring disabled", "reason", w.reason)
			return w
		}
		if _, err := os.Stat(o.KeyFile); err != nil {
			w.reason = "ssh mirrors configured but the key file is missing (" + o.KeyFile + ")"
			o.Log.Warn("mirroring disabled", "reason", w.reason)
			return w
		}
	}
	w.enabled = true
	if o.Metrics != nil {
		o.Metrics.MirrorsConfigured.Set(float64(len(o.Targets)))
	}
	return w
}

// Enabled reports whether the worker will mirror anything.
func (w *Worker) Enabled() bool { return w.enabled }

// Reason says why the worker is disabled ("" when enabled).
func (w *Worker) Reason() string { return w.reason }

// OnPush is the forge.OnPush hook: schedules a mirror push for a repository
// this node leads. Cheap and non-blocking.
func (w *Worker) OnPush(r *store.Repo) {
	if !w.enabled || r == nil {
		return
	}
	repo := r.Owner + "/" + r.Name
	if _, ok := w.o.Targets[repo]; !ok {
		return
	}
	w.schedule(repo, w.o.Now().Add(w.o.Debounce))
}

func (w *Worker) schedule(repo string, due time.Time) {
	w.mu.Lock()
	e := w.queue[repo]
	if e == nil {
		w.queue[repo] = &entry{due: due}
	} else if due.Before(e.due) {
		e.due = due
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Pending reports whether a mirror push is queued for repo, with its
// attempt count and due time (tests, admin status).
func (w *Worker) Pending(repo string) (attempts int, due time.Time, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.queue[repo]
	if !ok {
		return 0, time.Time{}, false
	}
	return e.attempts, e.due, true
}

// Run drives the queue until ctx ends: an initial and then periodic
// reconcile of every mirror, plus push-triggered entries and retries.
func (w *Worker) Run(ctx context.Context) {
	if !w.enabled {
		return
	}
	w.loadState(ctx)
	w.reconcile()
	reconcile := time.NewTicker(w.o.Interval)
	defer reconcile.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			w.reconcile()
		case <-w.wake:
		case <-tick.C:
		}
		w.RunDue(ctx, w.o.Now())
	}
}

// reconcile queues every configured mirror now.
func (w *Worker) reconcile() {
	now := w.o.Now()
	for repo := range w.o.Targets {
		w.schedule(repo, now)
	}
}

// RunDue performs every queued push whose time has come, one at a time,
// re-queueing failures with backoff. Run calls it; tests call it directly.
func (w *Worker) RunDue(ctx context.Context, now time.Time) {
	w.mu.Lock()
	var due []string
	for repo, e := range w.queue {
		if !e.due.After(now) {
			due = append(due, repo)
		}
	}
	sort.Strings(due)
	w.mu.Unlock()
	for _, repo := range due {
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		e := w.queue[repo]
		delete(w.queue, repo)
		w.mu.Unlock()
		if e == nil {
			continue
		}
		err := w.Push(ctx, repo)
		switch {
		case err == nil, errors.Is(err, ErrNotLeader), errors.Is(err, ErrNoMirror):
			continue
		}
		attempts := e.attempts + 1
		delay := w.o.Backoff[min(attempts, len(w.o.Backoff))-1]
		retry := w.o.Now().Add(delay)
		w.mu.Lock()
		if cur, ok := w.queue[repo]; ok {
			cur.attempts = attempts
			if retry.Before(cur.due) {
				cur.due = retry
			}
		} else {
			w.queue[repo] = &entry{due: retry, attempts: attempts}
		}
		w.mu.Unlock()
		w.o.Log.Warn("mirror push failed", "repo", repo, "attempt", attempts, "retry_in", delay.String(), "err", err)
	}
}

// Push mirrors one repository now and returns the outcome. It refuses
// (ErrNotLeader) on a node that does not lead the repository.
func (w *Worker) Push(ctx context.Context, repo string) error {
	target, ok := w.o.Targets[repo]
	if !ok {
		return ErrNoMirror
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("mirror: bad repository %q", repo)
	}
	if w.o.Leads != nil {
		leads, err := w.o.Leads(ctx, repo)
		if err != nil {
			return fmt.Errorf("mirror: %s: %w", repo, err)
		}
		if !leads {
			return ErrNotLeader
		}
	}
	dir := filepath.Join(w.o.ReposDir, owner, name+".git")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("mirror: %s: repository directory missing", repo)
	}
	ctx, cancel := context.WithTimeout(ctx, w.o.Timeout)
	defer cancel()
	release, err := w.o.Git.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("mirror: %s: no git slot: %w", repo, err)
	}
	defer release()
	start := w.o.Now()
	cmd := w.o.Git.Command(ctx, dir, "push", "--mirror", "--quiet", target)
	cmd.Env = w.env(target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.observe("error")
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return fmt.Errorf("mirror: %s -> %s: %w: %s", repo, Redact(target), err, msg)
	}
	w.observe("ok")
	now := w.o.Now()
	if w.o.Metrics != nil {
		w.o.Metrics.MirrorLastSuccess.WithLabelValues(repo).Set(float64(now.Unix()))
	}
	if w.o.Store != nil {
		if err := w.o.Store.SetSetting(ctx, settingKey(repo), strconv.FormatInt(now.Unix(), 10)); err != nil {
			w.o.Log.Warn("record mirror success", "repo", repo, "err", err)
		}
	}
	w.o.Log.Info("mirrored", "repo", repo, "remote", Redact(target), "ms", now.Sub(start).Milliseconds())
	return nil
}

func (w *Worker) observe(result string) {
	if w.o.Metrics != nil {
		w.o.Metrics.MirrorPushes.WithLabelValues(result).Inc()
	}
}

func settingKey(repo string) string { return "mirror.last_success." + repo }

// loadState restores the last-success gauges from settings after a restart.
func (w *Worker) loadState(ctx context.Context) {
	if w.o.Store == nil || w.o.Metrics == nil {
		return
	}
	for repo := range w.o.Targets {
		v, err := w.o.Store.Setting(ctx, settingKey(repo))
		if err != nil || v == "" {
			continue
		}
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			w.o.Metrics.MirrorLastSuccess.WithLabelValues(repo).Set(float64(ts))
		}
	}
}

// env is the hardened git environment with the transport this one command
// needs opened: ssh (with the deploy key and pinned host keys) or https for
// real remotes, file for a local path (tests only; the config layer refuses
// such URLs).
func (w *Worker) env(target string) []string {
	var cfg [][2]string
	var extra []string
	switch {
	case isSSH(target):
		cfg = append(cfg, [2]string{"protocol.ssh.allow", "always"})
		extra = append(extra, "GIT_SSH_COMMAND=ssh -i "+w.o.KeyFile+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+w.o.KnownHostsFile+" -o BatchMode=yes -o ConnectTimeout=20")
	case strings.HasPrefix(target, "https://"):
		cfg = append(cfg, [2]string{"protocol.https.allow", "always"})
	default:
		cfg = append(cfg, [2]string{"protocol.file.allow", "always"})
	}
	return withConfig(w.o.Git.Env(extra...), cfg...)
}

// withConfig appends GIT_CONFIG_KEY/VALUE pairs to an environment built by
// git.Backend.Env, renumbering GIT_CONFIG_COUNT. Later entries win for
// single-valued keys, so these override the hardened defaults.
func withConfig(env []string, kv ...[2]string) []string {
	n, idx := 0, -1
	out := make([]string, len(env), len(env)+2*len(kv)+1)
	copy(out, env)
	for i, e := range env {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			n, _ = strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT="))
			idx = i
		}
	}
	for j, p := range kv {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n+j, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n+j, p[1]))
	}
	count := "GIT_CONFIG_COUNT=" + strconv.Itoa(n+len(kv))
	if idx >= 0 {
		out[idx] = count
	} else {
		out = append(out, count)
	}
	return out
}

func isSSH(u string) bool {
	return strings.HasPrefix(u, "ssh://") || (!strings.Contains(u, "://") && strings.Contains(u, ":") && !strings.HasPrefix(u, "/"))
}

// Redact strips userinfo from a remote URL for logs.
func Redact(target string) string {
	if u, err := url.Parse(target); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		return u.String()
	}
	return target
}
