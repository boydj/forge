package repl

import (
	"context"
	"errors"
	"net/http"
)

// Write forwarding (ADR 0011): a Titan write arriving at a node that does not
// lead the repository is re-issued to the leader over the control plane with
// the original request and the client's certificate, and the leader runs
// authorisation and the write from scratch (threat model T-37: a replica can
// only replay what a real client sent it). Push forwarding is not in v1;
// pushes to replicas are refused with the leader's name.

// ForwardRequest is the body of POST /v1/forward.
type ForwardRequest struct {
	// Node is the replica that received the client request.
	Node string `json:"node"`
	// CertDER is the client's leaf certificate, raw DER.
	CertDER []byte `json:"cert_der"`
	// Path is the Titan request path (without scheme and host).
	Path string `json:"path"`
	// Mime is the Titan mime parameter; Body the uploaded bytes.
	Mime string `json:"mime"`
	Body []byte `json:"body"`
	// Token is the Titan token parameter, if any.
	Token string `json:"token,omitempty"`
}

// ForwardResponse is the leader's answer: a Gemini status and meta line to
// relay to the client verbatim.
type ForwardResponse struct {
	Status int    `json:"status"`
	Meta   string `json:"meta"`
}

// Handler executes a forwarded write on the leader. The web layer implements
// it by re-running its Titan handler with an identity derived from certDER.
type Handler interface {
	ForwardedWrite(ctx context.Context, certDER []byte, path, mime string, body []byte) (status int, meta string)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, certDER []byte, path, mime string, body []byte) (int, string)

// ForwardedWrite implements Handler.
func (f HandlerFunc) ForwardedWrite(ctx context.Context, certDER []byte, path, mime string, body []byte) (int, string) {
	return f(ctx, certDER, path, mime, body)
}

// Forwarder is what the web layer holds to forward writes; *Node implements
// it. A nil Forwarder means the cluster is disabled.
type Forwarder interface {
	Forward(ctx context.Context, leaderNode, path, mime string, body, certDER []byte) (status int, meta string, err error)
}

// Forward sends a Titan write to leaderNode and returns the leader's
// Gemini status and meta. A transport error (leader down) is returned as err
// so the caller can answer the client with a clear temporary failure.
func (n *Node) Forward(ctx context.Context, leaderNode, path, mime string, body, certDER []byte) (int, string, error) {
	if leaderNode == n.opts.Name {
		return 0, "", errors.New("repl: forward to self")
	}
	req := ForwardRequest{Node: n.opts.Name, CertDER: certDER, Path: path, Mime: mime, Body: body}
	var resp ForwardResponse
	if err := n.postJSON(ctx, leaderNode, "/v1/forward", req, &resp); err != nil {
		return 0, "", err
	}
	if resp.Status == 0 {
		return 0, "", errors.New("repl: leader returned no status")
	}
	return resp.Status, resp.Meta, nil
}

func (n *Node) handleForward(w http.ResponseWriter, r *http.Request, peer string) {
	if n.opts.Forward == nil {
		http.Error(w, "forwarding not enabled on this node", http.StatusNotImplemented)
		return
	}
	var req ForwardRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if req.Node != peer {
		http.Error(w, "node mismatch", http.StatusForbidden)
		return
	}
	if req.Path == "" || len(req.CertDER) == 0 {
		http.Error(w, "path and certificate are required", http.StatusBadRequest)
		return
	}
	status, meta := n.opts.Forward.ForwardedWrite(r.Context(), req.CertDER, req.Path, req.Mime, req.Body)
	n.log.Info("forwarded write", "from", peer, "path", req.Path, "status", status)
	writeJSON(w, ForwardResponse{Status: status, Meta: meta})
}
