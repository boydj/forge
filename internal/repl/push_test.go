package repl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

// echoPush is a PushHandler that reads stdin to EOF, echoes it on stdout
// with a prefix, reports on stderr and exits with a fixed code.
type echoPush struct {
	mu    sync.Mutex
	got   PushRequest
	calls atomic.Int32
	code  int
}

func (e *echoPush) ServeForwardedPush(_ context.Context, req PushRequest, stdin io.Reader, stdout, stderr io.Writer) int {
	e.calls.Add(1)
	e.mu.Lock()
	e.got = req
	e.mu.Unlock()
	data, err := io.ReadAll(stdin)
	if err != nil {
		_, _ = io.WriteString(stderr, "read: "+err.Error())
		return 99
	}
	_, _ = io.WriteString(stdout, "out:")
	// Uneven pieces so frames and chunking are exercised.
	for len(data) > 0 {
		n := 70000
		if n > len(data) {
			n = len(data)
		}
		_, _ = stdout.Write(data[:n])
		data = data[n:]
	}
	_, _ = io.WriteString(stderr, "err:done")
	return e.code
}

func pushTestNode(t *testing.T, name string, peers map[string]string, h PushHandler) *Node {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "forge.db"), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g, err := gitvcs.New(gitvcs.Options{HomeDir: dir, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{Name: name, Peers: peers, Secret: testSecret, ReposDir: filepath.Join(dir, "repos"), Version: "test", Store: st, Git: g, Push: h})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// pushPair builds a (client only) and b (control server running with the
// push handler installed before it serves; nil for none). It returns both
// nodes and b's address.
func pushPair(t *testing.T, handler PushHandler) (*Node, *Node, string) {
	t.Helper()
	lb, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bAddr := lb.Addr().String()
	aAddr := "127.0.0.1:1" // never dialled: b only needs a name->address binding for auth
	a := pushTestNode(t, "a", map[string]string{"b": bAddr}, nil)
	b := pushTestNode(t, "b", map[string]string{"a": aAddr}, handler)
	srv := &http.Server{Handler: b.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lb) }()
	t.Cleanup(func() { _ = srv.Close() })
	return a, b, bAddr
}

func TestForwardPushStreamsAndExitStatus(t *testing.T) {
	h := &echoPush{code: 5}
	a, _, _ := pushPair(t, h)
	payload := make([]byte, 300*1024+17)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	req := PushRequest{Owner: "alice", Repo: "proj", AccountID: 7, Account: "alice", Fingerprint: "SHA256:abc", RemoteIP: "203.0.113.9", GitProtocol: "version=2"}
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := a.ForwardPush(ctx, "b", req, bytes.NewReader(payload), &out, &errb)
	if err != nil {
		t.Fatalf("ForwardPush: %v", err)
	}
	if code != 5 {
		t.Errorf("exit code %d, want 5", code)
	}
	if !bytes.Equal(out.Bytes(), append([]byte("out:"), payload...)) {
		t.Errorf("stdout mismatch: got %d bytes, want %d", out.Len(), len(payload)+4)
	}
	if errb.String() != "err:done" {
		t.Errorf("stderr %q", errb.String())
	}
	h.mu.Lock()
	got := h.got
	h.mu.Unlock()
	want := req
	want.Node = "a"
	if got != want {
		t.Errorf("leader saw %+v, want %+v", got, want)
	}
}

func TestForwardPushFailures(t *testing.T) {
	a, _, _ := pushPair(t, nil) // b serves but has no push handler
	req := PushRequest{Owner: "alice", Repo: "proj", AccountID: 7, Account: "alice"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	discard := func() (io.Reader, io.Writer, io.Writer) { return strings.NewReader(""), io.Discard, io.Discard }

	// The leader answers but does not run pushes: a remote error, not unreachable.
	in, out, errw := discard()
	if _, err := a.ForwardPush(ctx, "b", req, in, out, errw); !errors.Is(err, ErrRemote) || !strings.Contains(err.Error(), "501") {
		t.Errorf("no handler: %v", err)
	}
	// A request a push-capable leader refuses before the upgrade.
	withHandler, _, _ := pushPair(t, &echoPush{})
	in, out, errw = discard()
	if _, err := withHandler.ForwardPush(ctx, "b", PushRequest{Owner: "alice"}, in, out, errw); !errors.Is(err, ErrRemote) || !strings.Contains(err.Error(), "400") {
		t.Errorf("bad request: %v", err)
	}
	in, out, errw = discard()
	if _, err := a.ForwardPush(ctx, "zz", req, in, out, errw); !errors.Is(err, ErrUnknownPeer) {
		t.Errorf("unknown peer: %v", err)
	}
	in, out, errw = discard()
	if _, err := a.ForwardPush(ctx, "a", req, in, out, errw); err == nil {
		t.Error("forward to self succeeded")
	}
	// Leader down: a port nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	_ = l.Close()
	down := pushTestNode(t, "a", map[string]string{"c": dead}, nil)
	in, out, errw = discard()
	if _, err := down.ForwardPush(ctx, "c", req, in, out, errw); !errors.Is(err, ErrUnreachable) {
		t.Errorf("dead leader: %v", err)
	}
}

func TestForwardPushRequiresAuth(t *testing.T) {
	h := &echoPush{}
	_, _, bAddr := pushPair(t, h)
	try := func(secret, node string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+bAddr+pushPath+"?owner=alice&repo=proj&account_id=7&account=alice", http.NoBody)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		req.Header.Set(headerNode, node)
		req.Header.Set("Upgrade", pushUpgrade)
		req.Header.Set("Connection", "Upgrade")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		// On 101 the body is the raw stream and the handler waits for our
		// EOF, so close without draining.
		if resp.StatusCode != http.StatusSwitchingProtocols {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := try("", "a"); code != http.StatusUnauthorized {
		t.Errorf("no secret: %d", code)
	}
	if code := try("wrong-secret-0123456789abcdef", "a"); code != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", code)
	}
	if code := try(testSecret, "mallory"); code != http.StatusForbidden {
		t.Errorf("unknown node: %d", code)
	}
	// Right secret, but "a" is bound to 127.0.0.1:1 and we come from
	// another port on 127.0.0.1: the host matches, so this is accepted by
	// authenticate (the binding is by host, not port). It then upgrades.
	if code := try(testSecret, "a"); code != http.StatusSwitchingProtocols {
		t.Errorf("valid request: %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := h.calls.Load(); n != 1 {
		t.Errorf("handler ran %d times, want 1 (unauthenticated requests must not reach it)", n)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	mu := &sync.Mutex{}
	bw := bufio.NewWriter(&buf)
	w := &frameWriter{mu: mu, w: bw, kind: frameStderr}
	big := bytes.Repeat([]byte("x"), frameChunk*2+5)
	if n, err := w.Write(big); err != nil || n != len(big) {
		t.Fatalf("write: %d %v", n, err)
	}
	mu.Lock()
	_ = writeFrame(bw, frameExit, []byte{3})
	_ = bw.Flush()
	mu.Unlock()
	br := bufio.NewReader(&buf)
	var got []byte
	for {
		kind, payload, err := readFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		if kind == frameExit {
			if payload[0] != 3 {
				t.Errorf("exit %d", payload[0])
			}
			break
		}
		if kind != frameStderr {
			t.Errorf("kind %d", kind)
		}
		if len(payload) > frameChunk {
			t.Errorf("chunk %d > %d", len(payload), frameChunk)
		}
		got = append(got, payload...)
	}
	if !bytes.Equal(got, big) {
		t.Errorf("payload mismatch: %d bytes", len(got))
	}
}
