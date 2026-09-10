// Package repl implements v1 replication between forge nodes: a small
// authenticated HTTP control plane on the WireGuard mesh, a pull-based
// replica worker, git transfer over smart HTTP, write forwarding to the
// leader, and the operator procedures behind `forge admin`. The model is
// described in docs/replication.md and ADR 0011: one leader per repository,
// replicas pull, no consensus.
//
// HTTP is used here because the control network is private (WireGuard or
// loopback, never a public interface), every request is authenticated with
// the cluster secret, and git's smart-HTTP transport gives us replication
// fetches for free. ADR 0009 allows HTTP internally "where clearly simplest".
package repl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"as215520.net/forge/internal/metrics"
	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

// Options configure a Node.
type Options struct {
	// Name is this node's name (config Node).
	Name string
	// Listen is the control-plane address (config Cluster.ControlListen).
	// Empty disables the server (admin-only use of the package).
	Listen string
	// Peers maps peer node name to control address (config Cluster.Peers).
	// It must not contain this node.
	Peers map[string]string
	// SecretFile holds the shared cluster secret. Secret may be given
	// directly instead (tests).
	SecretFile string
	Secret     string
	// MetadataLeader names the node that owns users, certificates and SSH
	// keys (config Cluster.MetadataLeader). Default: the first name in the
	// sorted set of peers plus this node.
	MetadataLeader string
	// SyncInterval is the replica poll period (default 10s).
	SyncInterval time.Duration
	// ReposDir is the root of bare repositories (<owner>/<name>.git).
	ReposDir string
	// AssetsDir is the root of release asset files
	// (<owner>/<name>/<tag>/<asset>), config AssetsDir(). Default: the
	// "assets" sibling of ReposDir, which is the standard data layout.
	AssetsDir string
	// Version is reported in /v1/status.
	Version string

	Store   *store.Store
	Git     *gitvcs.Backend
	Log     *slog.Logger
	Metrics *metrics.Registry
	// Forward handles writes forwarded from replicas (set by the web layer;
	// nil means /v1/forward answers 501).
	Forward Handler
}

// Node is the replication service of one forge node.
type Node struct {
	opts   Options
	secret []byte
	peers  []string // sorted peer names
	meta   string   // metadata leader
	log    *slog.Logger
	client *http.Client
	// assetClient has no overall timeout: asset downloads are bounded per
	// request by size in fetchAsset.
	assetClient *http.Client

	syncMu sync.Mutex // one sync cycle at a time
	wake   chan int64 // repo ids to sync promptly (0: everything)

	srv *http.Server
}

// Errors.
var (
	ErrNotLeader    = errors.New("repl: this node does not lead the repository")
	ErrUnknownPeer  = errors.New("repl: unknown peer")
	ErrNotSynced    = errors.New("repl: replica is not fully synced")
	ErrUnauthorized = errors.New("repl: unauthorized")
	ErrRemote       = errors.New("repl: remote error")
)

// New builds a Node. It loads the secret and validates the peer list but does
// not listen or start the worker; call Start for that.
func New(o Options) (*Node, error) {
	if o.Name == "" || o.Store == nil || o.Git == nil || o.ReposDir == "" {
		return nil, errors.New("repl: name, store, git and repos dir are required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = 10 * time.Second
	}
	if o.AssetsDir == "" {
		o.AssetsDir = filepath.Join(filepath.Dir(filepath.Clean(o.ReposDir)), "assets")
	}
	secret := strings.TrimSpace(o.Secret)
	if secret == "" && o.SecretFile != "" {
		b, err := os.ReadFile(o.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("repl: secret: %w", err)
		}
		secret = strings.TrimSpace(string(b))
	}
	if len(secret) < 16 {
		return nil, errors.New("repl: cluster secret must be at least 16 characters")
	}
	names := []string{o.Name}
	for name, addr := range o.Peers {
		if name == o.Name {
			return nil, fmt.Errorf("repl: peers must not include this node %q", name)
		}
		if name == "" || strings.ContainsAny(name, "/ \t\r\n") || addr == "" {
			return nil, fmt.Errorf("repl: bad peer %q=%q", name, addr)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	meta := o.MetadataLeader
	if meta == "" {
		meta = names[0]
	} else if _, ok := o.Peers[meta]; !ok && meta != o.Name {
		return nil, fmt.Errorf("repl: metadata leader %q is not this node or a peer", meta)
	}
	peers := make([]string, 0, len(o.Peers))
	for name := range o.Peers {
		peers = append(peers, name)
	}
	sort.Strings(peers)
	n := &Node{
		opts:        o,
		secret:      []byte(secret),
		peers:       peers,
		meta:        meta,
		log:         o.Log.With("component", "repl"),
		client:      &http.Client{Timeout: 2 * time.Minute},
		assetClient: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}},
		wake:        make(chan int64, 64),
	}
	return n, nil
}

// Name returns this node's name.
func (n *Node) Name() string { return n.opts.Name }

// MetadataLeader returns the node that owns global metadata.
func (n *Node) MetadataLeader() string { return n.meta }

// Peers returns the sorted peer names.
func (n *Node) Peers() []string { return append([]string(nil), n.peers...) }

// PeerAddr returns the control address of a peer.
func (n *Node) PeerAddr(name string) (string, error) {
	addr, ok := n.opts.Peers[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPeer, name)
	}
	return addr, nil
}

// SetForwardHandler installs the handler for forwarded writes.
func (n *Node) SetForwardHandler(h Handler) { n.opts.Forward = h }

// RepoPath is the on-disk location of a repository.
func (n *Node) RepoPath(owner, name string) string {
	return filepath.Join(n.opts.ReposDir, owner, name+".git")
}

// AssetPath is the on-disk location of a release asset, laid out exactly
// like forge.AssetPath. Callers must validate tag and name (validAsset)
// first; this function does not.
func (n *Node) AssetPath(rp *store.Repo, tag, name string) string {
	return filepath.Join(n.opts.AssetsDir, rp.Owner, rp.Name, tag, name)
}

// Start listens on the control address and runs the replica worker until ctx
// is cancelled. It returns once the listener is bound; errors from serving
// are reported through errc if non-nil.
func (n *Node) Start(ctx context.Context, errc chan<- error) error {
	if n.opts.Listen == "" {
		return errors.New("repl: no control listen address")
	}
	l, err := net.Listen("tcp", n.opts.Listen)
	if err != nil {
		return fmt.Errorf("repl: listen %s: %w", n.opts.Listen, err)
	}
	n.log.Info("listening", "proto", "control", "addr", l.Addr(), "metadata_leader", n.meta, "peers", n.peers)
	n.srv = &http.Server{
		Handler:           n.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		err := n.srv.Serve(l)
		if errc != nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	go n.Run(ctx)
	return nil
}

// Addr returns the bound listen address after Start ("" before).
func (n *Node) Addr() string {
	if n.srv == nil {
		return ""
	}
	return n.srv.Addr
}

// Shutdown stops the control server.
func (n *Node) Shutdown(ctx context.Context) error {
	if n.srv == nil {
		return nil
	}
	return n.srv.Shutdown(ctx)
}

// OnPush is the forge.OnPush hook for leaders: it asks every peer to fetch
// the repository promptly instead of waiting for the next poll.
func (n *Node) OnPush(r *store.Repo) {
	if r == nil {
		return
	}
	go n.NotifyPeers(context.Background(), r.ID)
}

// NotifyPeers posts /v1/notify to every peer, best effort.
func (n *Node) NotifyPeers(ctx context.Context, repoID int64) {
	for _, p := range n.peers {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := n.postJSON(ctx, p, "/v1/notify", notifyRequest{RepoID: repoID}, nil)
		cancel()
		if err != nil {
			n.log.Debug("notify failed", "peer", p, "repo", repoID, "err", err)
		}
	}
}
