// Package hooks implements the git server-side hook protocol between the
// `forge hook` process (spawned by git-receive-pack) and the daemon.
//
// git runs pre-receive before any ref is updated, with the pushed objects
// in a quarantine directory; the daemon decides allow/deny. post-receive
// runs after the update and records events. Transport is one JSON request
// and one JSON response over the daemon's Unix socket.
package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Update is one ref change reported by git.
type Update struct {
	Old string `json:"old"`
	New string `json:"new"`
	Ref string `json:"ref"`
}

// Request is sent by the hook process.
type Request struct {
	Hook        string   `json:"hook"`
	AccountID   int64    `json:"account_id"`
	Account     string   `json:"account"`
	Repo        string   `json:"repo"` // owner/name
	Updates     []Update `json:"updates"`
	PushedBytes int64    `json:"pushed_bytes"`
}

// Response is returned by the daemon.
type Response struct {
	OK       bool     `json:"ok"`
	Messages []string `json:"messages,omitempty"`
}

// Env variable names shared with the SSH server.
const (
	EnvSocket    = "FORGE_HOOK_SOCKET"
	EnvAccountID = "FORGE_ACCOUNT_ID"
	EnvAccount   = "FORGE_ACCOUNT"
	EnvRepo      = "FORGE_REPO"
)

// ErrDenied is returned by Run when the daemon rejects the push.
var ErrDenied = errors.New("push rejected")

// Run executes the hook client: it reads ref updates from stdin, asks the
// daemon, prints messages to stderr and returns ErrDenied on rejection.
func Run(hook string, stdin io.Reader, stderr io.Writer) error {
	if hook == "update" {
		// pre-receive makes all decisions; update is a no-op.
		return nil
	}
	sock := os.Getenv(EnvSocket)
	if sock == "" {
		fmt.Fprintln(stderr, "forge: hook invoked outside the forge (no socket); refusing")
		return ErrDenied
	}
	req := Request{Hook: hook, Account: os.Getenv(EnvAccount), Repo: os.Getenv(EnvRepo)}
	req.AccountID, _ = strconv.ParseInt(os.Getenv(EnvAccountID), 10, 64)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		req.Updates = append(req.Updates, Update{Old: f[0], New: f[1], Ref: f[2]})
	}
	if q := os.Getenv("GIT_QUARANTINE_PATH"); q != "" && hook == "pre-receive" {
		req.PushedBytes = dirSize(q)
	}
	resp, err := call(sock, &req)
	if err != nil {
		fmt.Fprintf(stderr, "forge: hook error: %v\n", err)
		return ErrDenied
	}
	for _, m := range resp.Messages {
		fmt.Fprintln(stderr, m)
	}
	if !resp.OK {
		return ErrDenied
	}
	return nil
}

func call(sock string, req *Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

// Handler decides hook requests (implemented by the forge).
type Handler interface {
	HandleHook(ctx context.Context, req *Request) *Response
}

// Server accepts hook connections on a Unix socket.
type Server struct {
	Handler Handler
	l       net.Listener
}

// Listen creates the socket (removing a stale one) with owner-only access.
func (s *Server) Listen(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return err
	}
	s.l = l
	return nil
}

// Serve handles connections until Close.
func (s *Server) Serve() error {
	for {
		conn, err := s.l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// Close stops the server.
func (s *Server) Close() error {
	if s.l == nil {
		return nil
	}
	return s.l.Close()
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	var req Request
	dec := json.NewDecoder(io.LimitReader(conn, 1<<20))
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(&Response{OK: false, Messages: []string{"forge: bad hook request"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	resp := s.Handler.HandleHook(ctx, &req)
	_ = json.NewEncoder(conn).Encode(resp)
}
