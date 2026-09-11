package repl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Push forwarding (docs/replication.md, Forwarding). A replica that
// receives `git push` for a repository it does not lead relays the whole
// receive-pack session to the leader over the control plane: one HTTP
// request upgraded to a raw byte stream. Replica -> leader carries the
// client's stdin verbatim (half-closed when the client sends EOF); leader ->
// replica is framed so stdout, stderr and the exit status stay separate.
//
// Frame: 1 byte kind (1 stdout, 2 stderr, 3 exit), 4 bytes big-endian
// length, payload. The exit frame carries one byte and is always last.
//
// Trust model: the replica asserts an identity it authenticated by SSH key;
// the leader re-runs every other check (repository, permission, leadership,
// hooks). A compromised replica can therefore push as any user whose key
// it accepted, which is the same exposure forwarded Titan writes have
// (threat model T-37) and no more than the replica already has locally.

const (
	pushPath    = "/v1/forward/receive-pack"
	pushUpgrade = "forge-receive-pack/1"

	frameStdout byte = 1
	frameStderr byte = 2
	frameExit   byte = 3

	// maxFrame bounds one frame payload; the writer chunks at frameChunk.
	maxFrame   = 1 << 20
	frameChunk = 64 << 10

	pushDialTimeout = 10 * time.Second
)

// ErrUnreachable means the leader could not be reached or the connection
// to it was lost before the push completed.
var ErrUnreachable = errors.New("repl: leader unreachable")

// PushRequest identifies a forwarded receive-pack session.
type PushRequest struct {
	// Node is the replica relaying the push (set by the leader from the
	// authenticated peer name).
	Node        string
	Owner, Repo string
	AccountID   int64
	Account     string
	Fingerprint string
	RemoteIP    string
	// GitProtocol is the client's GIT_PROTOCOL value ("version=2" or "").
	GitProtocol string
}

// PushHandler runs a forwarded push on the leader; the SSH server
// implements it. It returns the exit status to relay to the client.
type PushHandler interface {
	ServeForwardedPush(ctx context.Context, req PushRequest, stdin io.Reader, stdout, stderr io.Writer) int
}

// SetPushHandler installs the handler for forwarded pushes. Call it before
// Start.
func (n *Node) SetPushHandler(h PushHandler) { n.opts.Push = h }

func (r PushRequest) query() url.Values {
	q := url.Values{}
	q.Set("owner", r.Owner)
	q.Set("repo", r.Repo)
	q.Set("account_id", strconv.FormatInt(r.AccountID, 10))
	q.Set("account", r.Account)
	q.Set("fingerprint", r.Fingerprint)
	q.Set("remote", r.RemoteIP)
	q.Set("protocol", r.GitProtocol)
	return q
}

func pushRequestFromQuery(q url.Values, peer string) PushRequest {
	id, _ := strconv.ParseInt(q.Get("account_id"), 10, 64)
	return PushRequest{Node: peer, Owner: q.Get("owner"), Repo: q.Get("repo"), AccountID: id, Account: q.Get("account"),
		Fingerprint: q.Get("fingerprint"), RemoteIP: q.Get("remote"), GitProtocol: q.Get("protocol")}
}

// ForwardPush relays a receive-pack session to leader and returns the exit
// status of the leader's receive-pack. A transport failure is returned as
// an error wrapping ErrUnreachable (before or during the session; the
// caller cannot tell whether the push was applied) or ErrRemote (the leader
// answered but refused to run it); nothing is written to stdout then.
func (n *Node) ForwardPush(ctx context.Context, leader string, req PushRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if leader == n.opts.Name {
		return 0, errors.New("repl: forward to self")
	}
	addr, err := n.PeerAddr(leader)
	if err != nil {
		return 0, err
	}
	d := net.Dialer{Timeout: pushDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrUnreachable, leader, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	hreq, err := http.NewRequest(http.MethodPost, "http://"+addr+pushPath+"?"+req.query().Encode(), http.NoBody)
	if err != nil {
		return 0, err
	}
	hreq.Header.Set("Authorization", "Bearer "+string(n.secret))
	hreq.Header.Set(headerNode, n.opts.Name)
	hreq.Header.Set("Upgrade", pushUpgrade)
	hreq.Header.Set("Connection", "Upgrade")
	_ = conn.SetDeadline(time.Now().Add(pushDialTimeout))
	if err := hreq.Write(conn); err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrUnreachable, leader, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, hreq)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrUnreachable, leader, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return 0, fmt.Errorf("%w: POST %s: %s: %s", ErrRemote, pushPath, resp.Status, string(msg))
	}
	_ = conn.SetDeadline(time.Time{})

	// Client -> leader: raw stdin, half-closed on EOF so receive-pack sees
	// the end of the pack. The goroutine ends when stdin's source is closed
	// by the caller or the connection goes away.
	go func() {
		_, _ = io.Copy(conn, stdin)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	// Leader -> client: demultiplex frames until the exit frame.
	for {
		kind, payload, err := readFrame(br)
		if err != nil {
			if ctx.Err() != nil {
				return 0, fmt.Errorf("%w: %s: %v", ErrUnreachable, leader, ctx.Err())
			}
			return 0, fmt.Errorf("%w: %s: connection lost: %v", ErrUnreachable, leader, err)
		}
		switch kind {
		case frameStdout:
			if _, err := stdout.Write(payload); err != nil {
				return 0, err
			}
		case frameStderr:
			_, _ = stderr.Write(payload)
		case frameExit:
			if len(payload) != 1 {
				return 0, fmt.Errorf("%w: %s: malformed exit frame", ErrRemote, leader)
			}
			return int(payload[0]), nil
		default:
			return 0, fmt.Errorf("%w: %s: unknown frame %d", ErrRemote, leader, kind)
		}
	}
}

// handleForwardPush runs a relayed push on this node (the leader).
func (n *Node) handleForwardPush(w http.ResponseWriter, r *http.Request, peer string) {
	if n.opts.Push == nil {
		http.Error(w, "push forwarding not enabled on this node", http.StatusNotImplemented)
		return
	}
	if r.Header.Get("Upgrade") != pushUpgrade {
		http.Error(w, "expected Upgrade: "+pushUpgrade, http.StatusBadRequest)
		return
	}
	req := pushRequestFromQuery(r.URL.Query(), peer)
	if req.Owner == "" || req.Repo == "" || req.AccountID <= 0 || req.Account == "" {
		http.Error(w, "owner, repo, account_id and account are required", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		n.serverError(w, err)
		return
	}
	defer conn.Close()
	// The server's header read deadline is still armed on the raw conn.
	_ = conn.SetDeadline(time.Time{})
	if _, err := bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + pushUpgrade + "\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}
	// stdin comes from the raw connection, never through bufrw.Reader: that
	// reader is backed by the server's connReader, which cancels the request
	// context on any read error, including the EOF of the replica's
	// half-close once the client has sent the whole pack. Reading it there
	// would kill receive-pack exactly when it starts doing its work. Bytes
	// the server already buffered are drained first without touching the
	// underlying reader.
	var stdin io.Reader = conn
	if buffered := bufrw.Reader.Buffered(); buffered > 0 {
		head, _ := bufrw.Reader.Peek(buffered)
		stdin = io.MultiReader(bytes.NewReader(append([]byte(nil), head...)), conn)
	}
	mu := &sync.Mutex{}
	stdout := &frameWriter{mu: mu, w: bufrw.Writer, kind: frameStdout}
	stderr := &frameWriter{mu: mu, w: bufrw.Writer, kind: frameStderr}
	start := time.Now()
	code := n.opts.Push.ServeForwardedPush(r.Context(), req, stdin, stdout, stderr)
	if code < 0 || code > 255 {
		code = 255
	}
	mu.Lock()
	_ = writeFrame(bufrw.Writer, frameExit, []byte{byte(code)})
	_ = bufrw.Flush()
	mu.Unlock()
	n.log.Info("forwarded push", "from", peer, "repo", req.Owner+"/"+req.Repo, "account", req.Account, "exit", code, "ms", time.Since(start).Milliseconds())
}

// frameWriter frames writes of one kind onto a shared buffered writer.
type frameWriter struct {
	mu   *sync.Mutex
	w    *bufio.Writer
	kind byte
}

func (f *frameWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > frameChunk {
			chunk = p[:frameChunk]
		}
		f.mu.Lock()
		err := writeFrame(f.w, f.kind, chunk)
		if err == nil {
			err = f.w.Flush()
		}
		f.mu.Unlock()
		if err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

func writeFrame(w *bufio.Writer, kind byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(hdr[1:])
	if size > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}
