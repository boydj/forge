package web

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/url"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// captureWriter records a response header for forwarded writes.
type captureWriter struct {
	status int
	meta   string
	body   bytes.Buffer
}

func (c *captureWriter) Header(status int, meta string) error {
	if c.status != 0 {
		return errors.New("header already written")
	}
	c.status, c.meta = status, gemini.SanitizeMeta(meta)
	return nil
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status < 20 || c.status > 29 {
		return 0, errors.New("body without success header")
	}
	if c.body.Len() > 64<<10 {
		return len(p), nil
	}
	return c.body.Write(p)
}

func (c *captureWriter) Status() int { return c.status }

// ForwardedWrite implements repl.Handler: the leader re-runs a Titan write
// that arrived at another node, with the original client certificate so
// authorisation is evaluated here.
func (h *Handler) ForwardedWrite(ctx context.Context, certDER []byte, path, mime string, body []byte) (int, string) {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return gemini.StatusCertificateInvalid, "bad certificate"
	}
	u := &url.URL{Scheme: "titan", Host: h.F.Config.Hostname, Path: path}
	req := &gemini.Request{
		URL: u, Raw: u.String(), Certificate: cert, Fingerprint: gemini.CertificateFingerprint(cert),
		Titan: &gemini.TitanParams{Size: int64(len(body)), MIME: mime},
		Body:  bytes.NewReader(body),
	}
	cw := &captureWriter{}
	h.ServeGemini(ctx, cw, req)
	if cw.status == 0 {
		return gemini.StatusTemporaryFailure, "no response"
	}
	return cw.status, cw.meta
}

// forwardIfRemote relays the Titan request to the repository's leader when
// this node does not lead it. It reports whether the request was handled.
func (h *Handler) forwardIfRemote(req *request, r *store.Repo) bool {
	if h.Forwarder == nil || h.F.IsLeader(r) || req.Titan == nil || req.Titan.Edit {
		return false
	}
	if req.Titan.Size > h.F.Config.Limits.MaxTitanBytes {
		_ = req.w.Header(gemini.StatusPermanentFailure, "body too large")
		return true
	}
	body, err := io.ReadAll(req.Body)
	if err != nil || int64(len(body)) != req.Titan.Size {
		_ = gemini.BadRequest(req.w, "short body")
		return true
	}
	status, meta, err := h.Forwarder.Forward(req.ctx, r.LeaderNode, req.Path(), req.Titan.MIME, body, req.Certificate.Raw)
	if err != nil {
		h.Log.Warn("forward failed", "leader", r.LeaderNode, "path", req.Path(), "err", err)
		req.fail(forge.ErrNotLeader)
		return true
	}
	_ = req.w.Header(status, meta)
	return true
}
