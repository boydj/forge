// Package web serves the forge over Gemini and Titan: routing, page
// rendering in gemtext, feeds and Titan write endpoints. It is the only
// package that knows about URL layout (see ADR 0009).
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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

	limiter rateLimiter
}

// Forwarder is implemented by the replication node.
type Forwarder interface {
	Forward(ctx context.Context, leaderNode, path, mime string, body, certDER []byte) (status int, meta string, err error)
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
}

// New returns a handler.
func New(f *forge.Forge, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{F: f, Log: log, Started: time.Now()}
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
	path := r.Path()
	if strings.Contains(path, "/./") || strings.Contains(path, "\x00") {
		_ = gemini.BadRequest(w, "bad path")
		return
	}
	req.segs = splitPath(path)
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
	case segs[0] == "status" && len(segs) == 1:
		h.status(req)
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
	_ = req.w.Header(gemini.StatusSuccess, "text/gemini; charset=utf-8; lang=en")
	p := gemini.NewPage()
	if title != "" {
		p.Heading(1, title)
	}
	return p
}

func (req *request) send(p *gemini.Page) {
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
