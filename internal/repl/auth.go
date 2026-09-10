package repl

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Authentication is a shared cluster secret carried as a bearer token plus
// the caller's node name in X-Forge-Node. The secret is compared in constant
// time and the node must be a configured peer. This is deliberately simple:
// the control network is WireGuard (mutual node authentication at the
// transport, threat model T-33) and the secret only stops a stray process on
// the mesh from talking to the control plane. Per-message signatures and
// nonces (T-41) are future work.

const (
	headerNode   = "X-Forge-Node"
	maxJSONBody  = 64 << 20
	maxPeerLimit = 1000
)

// authenticate checks the request and returns the calling peer's name.
func (n *Node) authenticate(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), n.secret) != 1 {
		return "", ErrUnauthorized
	}
	peer := r.Header.Get(headerNode)
	if _, ok := n.opts.Peers[peer]; !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPeer, peer)
	}
	return peer, nil
}

// withAuth wraps a handler with authentication; the peer name is stored in
// the request context.
func (n *Node) withAuth(h func(w http.ResponseWriter, r *http.Request, peer string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, err := n.authenticate(r)
		if err != nil {
			status := http.StatusUnauthorized
			if strings.Contains(err.Error(), "unknown peer") {
				status = http.StatusForbidden
			}
			n.log.Warn("control auth failed", "remote", r.RemoteAddr, "path", r.URL.Path, "err", err)
			http.Error(w, err.Error(), status)
			return
		}
		h(w, r, peer)
	}
}

// newRequest builds an authenticated request to a peer.
func (n *Node) newRequest(ctx context.Context, peer, method, path string, body io.Reader) (*http.Request, error) {
	addr, err := n.PeerAddr(peer)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(n.secret))
	req.Header.Set(headerNode, n.opts.Name)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// getJSON performs GET and decodes a JSON response into out.
func (n *Node) getJSON(ctx context.Context, peer, path string, out any) error {
	req, err := n.newRequest(ctx, peer, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return n.do(req, out)
}

// postJSON performs POST with a JSON body and decodes the response into out
// (nil to discard).
func (n *Node) postJSON(ctx context.Context, peer, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := n.newRequest(ctx, peer, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return n.do(req, out)
}

func (n *Node) do(req *http.Request, out any) error {
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxJSONBody)
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(body, 4096))
		return fmt.Errorf("%w: %s %s: %s: %s", ErrRemote, req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	dec := json.NewDecoder(body)
	dec.UseNumber()
	return dec.Decode(out)
}

// writeJSON encodes v as the response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// Headers are already out; nothing more to do than log at the caller.
		return
	}
}

// readJSON decodes a bounded request body.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.UseNumber()
	return dec.Decode(v)
}
