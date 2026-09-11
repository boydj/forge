// Package web serves the forge over Gemini and Titan: routing, page
// rendering in gemtext, feeds and Titan write endpoints. It is the only
// package that knows about URL layout (see ADR 0009).
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/version"
)

// Handler routes Gemini/Titan requests.
type Handler struct {
	F       *forge.Forge
	Log     *slog.Logger
	Started time.Time
	// Health reports node health for /status; nil means healthy.
	Health func() (ok bool, detail string)
	// Forwarder relays Titan writes for repositories led elsewhere; nil in
	// single-node mode.
	Forwarder Forwarder
	// Fleet lists the cluster for the status page; nil shows this node only.
	Fleet FleetSource

	limiter rateLimiter
	secret  []byte
	titleCache
	alertCache alertCache
}

// Forwarder is implemented by the replication node.
type Forwarder interface {
	Forward(ctx context.Context, leaderNode, path, mime string, body, certDER []byte) (status int, meta string, err error)
}

// ipKey hashes a remote address into the limiter key space (negative keys
// never collide with user ids).
func ipKey(a net.Addr) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(ipOf(a)))
	return -int64(h.Sum64() >> 1)
}

func ipOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.String()
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}

// rateLimiter is a per-user sliding one-minute window for Titan writes.
type rateLimiter struct {
	mu   sync.Mutex
	hits map[int64][]time.Time
}

func (l *rateLimiter) allow(user int64, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[int64][]time.Time{}
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	kept := l.hits[user][:0]
	for _, t := range l.hits[user] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= perMinute {
		l.hits[user] = kept
		return false
	}
	l.hits[user] = append(kept, now)
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if len(v) == 0 || !v[len(v)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// request bundles per-request state.
type request struct {
	*gemini.Request
	ctx  context.Context
	w    gemini.ResponseWriter
	id   *forge.Identity
	auth error // result of Authenticate when a certificate was presented
	segs []string
	// pending is set by page(): the success header is written by send(), so
	// a handler that fails after starting a page can still answer 4x/5x.
	pending bool
	// actionOK is set when the request carried a valid action token
	// (/_/<token>/...): the query was typed by the user at an INPUT prompt
	// served by us, not pre-filled by a link (see action()).
	actionOK bool
}

// New returns a handler.
func New(f *forge.Forge, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{F: f, Log: log, Started: time.Now()}
	secret, err := loadActionSecret(f.Config)
	if err != nil {
		log.Error("action secret", "err", err)
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	h.secret = secret
	return h
}

// loadActionSecret returns the HMAC key for action tokens: the cluster
// secret when clustered (tokens must verify on every node behind anycast),
// otherwise a per-node secret generated once under the data directory.
func loadActionSecret(cfg *config.Config) ([]byte, error) {
	if cfg.Cluster.Enabled && cfg.Cluster.SecretFile != "" {
		b, err := os.ReadFile(cfg.Cluster.SecretFile)
		if err != nil {
			return nil, err
		}
		return []byte(strings.TrimSpace(string(b))), nil
	}
	path := filepath.Join(cfg.DataDir, "action.secret")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 16 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

// actionIdentity names the party consenting: the account id, or the
// certificate key before an account exists.
func (req *request) actionIdentity() string {
	if req.id != nil && req.id.User != nil {
		return fmt.Sprintf("u%d", req.id.User.ID)
	}
	return "c" + req.Fingerprint
}

func (h *Handler) actionToken(identity, path string, day time.Time) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%s\n%s\n%s", day.UTC().Format("2006-01-02"), identity, path)
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (h *Handler) verifyAction(identity, path, tok string) bool {
	now := time.Now()
	for _, d := range []time.Time{now, now.Add(-24 * time.Hour)} {
		if hmac.Equal([]byte(h.actionToken(identity, path, d)), []byte(tok)) {
			return true
		}
	}
	return false
}

// action gates a query-driven state change. Without a valid token in the
// path the request is redirected to the tokenised path with the query
// dropped, which forces the INPUT prompt: a link cannot pre-fill the value.
// With the token and no query it prompts; with both it returns the value.
func (req *request) action(h *Handler, prompt string, sensitive bool) (string, bool) {
	if req.Certificate == nil {
		_ = gemini.CertRequired(req.w, "a client certificate identifies you")
		return "", false
	}
	if !req.actionOK {
		tok := h.actionToken(req.actionIdentity(), req.URL.Path, time.Now())
		_ = gemini.Redirect(req.w, "/_/"+tok+req.URL.Path)
		return "", false
	}
	q := strings.TrimSpace(req.Query())
	if q == "" && !req.URL.ForceQuery {
		if sensitive {
			_ = req.w.Header(gemini.StatusSensitiveInput, prompt)
		} else {
			_ = gemini.Input(req.w, prompt)
		}
		return "", false
	}
	return q, true
}

// ServeGemini implements gemini.Handler.
func (h *Handler) ServeGemini(ctx context.Context, w gemini.ResponseWriter, r *gemini.Request) {
	req := &request{Request: r, ctx: ctx, w: w}
	req.id, req.auth = h.F.Authenticate(ctx, r.Certificate)
	if req.auth != nil && !errors.Is(req.auth, forge.ErrCertUnknown) {
		// Revoked, expired or disabled: refuse everything so the user notices.
		switch {
		case errors.Is(req.auth, forge.ErrCertRevoked):
			_ = w.Header(gemini.StatusCertificateInvalid, "certificate revoked")
		case errors.Is(req.auth, forge.ErrCertExpired):
			_ = w.Header(gemini.StatusCertificateInvalid, "certificate expired")
		case errors.Is(req.auth, forge.ErrDisabled):
			_ = w.Header(gemini.StatusCertificateNotAuth, "account disabled")
		default:
			h.Log.Error("authenticate", "err", req.auth)
			_ = w.Header(gemini.StatusTemporaryFailure, "identity lookup failed")
		}
		return
	}
	if !h.F.Config.ServesHost(r.URL.Hostname()) {
		_ = w.Header(gemini.StatusProxyRequestRefused, "this server does not proxy requests for "+r.URL.Hostname())
		return
	}
	path := r.Path()
	for _, c := range path {
		if c < 0x20 || c == 0x7f {
			_ = gemini.BadRequest(w, "bad path")
			return
		}
	}
	if strings.Contains(path, "/./") {
		_ = gemini.BadRequest(w, "bad path")
		return
	}
	req.segs = splitPath(path)
	if len(req.segs) >= 2 && req.segs[0] == "_" {
		rest := "/" + strings.Join(req.segs[2:], "/")
		if strings.HasSuffix(path, "/") && rest != "/" {
			rest += "/"
		}
		if !h.verifyAction(req.actionIdentity(), rest, req.segs[1]) {
			_ = gemini.Redirect(w, rest)
			return
		}
		req.actionOK = true
		r.URL.Path = rest
		req.segs = req.segs[2:]
	}
	if r.IsTitan() {
		h.serveTitan(req)
		return
	}
	h.route(req)
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// route dispatches Gemini reads.
func (h *Handler) route(req *request) {
	segs := req.segs
	path := req.Path()
	switch {
	case len(segs) == 0:
		h.front(req)
	case segs[0] == "feed" && len(segs) == 1:
		h.feedAll(req, false)
	case segs[0] == "atom.xml" && len(segs) == 1:
		h.feedAll(req, true)
	case segs[0] == "status":
		h.statusRoutes(req, segs[1:], strings.HasSuffix(path, "/"))
	case segs[0] == "docs":
		h.docs(req, segs[1:], strings.HasSuffix(path, "/"))
	case segs[0] == "new" && len(segs) == 1:
		h.newRepo(req)
	case segs[0] == "account":
		h.account(req, segs[1:])
	case strings.HasPrefix(segs[0], "~"):
		h.userRoutes(req, strings.TrimPrefix(segs[0], "~"), segs[1:], strings.HasSuffix(path, "/"))
	default:
		_ = gemini.NotFound(req.w)
	}
}

// page starts a gemtext response and returns the page builder.
func (req *request) page(title string) *gemini.Page {
	req.pending = true
	p := gemini.NewPage()
	if title != "" {
		p.Heading(1, title)
	}
	return p
}

func (req *request) send(p *gemini.Page) {
	if req.pending && req.w.Status() == 0 {
		_ = req.w.Header(gemini.StatusSuccess, "text/gemini; charset=utf-8; lang=en")
	}
	req.pending = false
	_, _ = req.w.Write(p.Bytes())
}

// fail maps domain errors to Gemini statuses.
func (req *request) fail(err error) {
	switch {
	case errors.Is(err, forge.ErrNotFound), errors.Is(err, store.ErrNotFound):
		_ = gemini.NotFound(req.w)
	case errors.Is(err, forge.ErrAuthRequired):
		_ = gemini.CertRequired(req.w, "a client certificate identifies you; see /account")
	case errors.Is(err, forge.ErrForbidden):
		_ = gemini.Forbidden(req.w, "not permitted")
	case errors.Is(err, forge.ErrInvalidName), errors.Is(err, forge.ErrReservedName):
		_ = gemini.BadRequest(req.w, "invalid name")
	case errors.Is(err, forge.ErrExists):
		_ = req.w.Header(gemini.StatusPermanentFailure, "already exists")
	case errors.Is(err, forge.ErrQuota):
		_ = req.w.Header(gemini.StatusPermanentFailure, "quota exceeded")
	case errors.Is(err, forge.ErrDiskFull):
		_ = req.w.Header(gemini.StatusServerUnavailable, "insufficient storage")
	case errors.Is(err, forge.ErrArchived):
		_ = req.w.Header(gemini.StatusPermanentFailure, "repository is archived")
	case errors.Is(err, forge.ErrTooLarge):
		_ = req.w.Header(gemini.StatusPermanentFailure, "too large")
	case errors.Is(err, forge.ErrNotAcceptable):
		_ = gemini.BadRequest(req.w, "not acceptable")
	case errors.Is(err, forge.ErrNotLeader):
		_ = req.w.Header(gemini.StatusTemporaryFailure, "writes are not accepted on this node right now")
	default:
		_ = gemini.TemporaryFailure(req.w, "internal error")
	}
}

// requireUser returns the account or sends 60/61.
func (req *request) requireUser() *store.User {
	if req.id != nil && req.id.User != nil {
		return req.id.User
	}
	if req.Certificate == nil {
		_ = gemini.CertRequired(req.w, "a client certificate identifies you; register at /account")
		return nil
	}
	_ = gemini.Forbidden(req.w, "this certificate is not registered; visit /account to register")
	return nil
}

func (h *Handler) footer(p *gemini.Page, req *request) {
	p.Blank()
	p.Link("/", h.F.Config.Title)
	if req.id != nil && req.id.User != nil {
		p.Link("/account", "signed in as "+req.id.User.Name)
	} else {
		p.Link("/account", "sign in / register")
	}
}

func (h *Handler) status(req *request) {
	ok, detail := true, "ok"
	if h.Health != nil {
		ok, detail = h.Health()
	}
	if !ok {
		_ = req.w.Header(gemini.StatusServerUnavailable, "unhealthy: "+detail)
		return
	}
	_ = req.w.Header(gemini.StatusSuccess, "text/plain; charset=utf-8")
	fmt.Fprintf(req.w, "ok\nnode: %s\nversion: %s\nuptime: %s\n", h.F.Config.Node, version.Version, time.Since(h.Started).Round(time.Second))
}

// when renders a time compactly.
func when(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func date(t time.Time) string { return t.UTC().Format("2006-01-02") }

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}
