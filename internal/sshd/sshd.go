// Package sshd is the restricted Git-over-SSH server. It authenticates
// clients by public key only, accepts exactly one kind of channel
// (session) and exactly one kind of work (exec of git-upload-pack or
// git-receive-pack on a validated owner/repo path), and never spawns a
// shell, allocates a PTY, opens a subsystem or forwards anything.
//
// See docs/git-ssh.md and the SSH sections of docs/threat-model.md.
package sshd

import (
	"context"
	"crypto/rsa"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/vcs/git"
)

// Account is the identity behind an authenticated SSH key.
type Account struct {
	ID   int64
	Name string
	// Fingerprint is the SHA256 fingerprint of the key used to authenticate.
	Fingerprint string
}

// ErrUnknownKey is returned by an Authenticator for a key that is not
// registered (or is revoked). The server reports every authentication
// failure to the client identically.
var ErrUnknownKey = errors.New("sshd: unknown key")

// Authenticator maps SSH public keys to accounts.
type Authenticator interface {
	// AuthenticateKey returns the account owning key (matched by the key's
	// ssh.MarshalAuthorizedKey form or SHA256 fingerprint) or ErrUnknownKey.
	AuthenticateKey(ctx context.Context, key ssh.PublicKey) (*Account, error)
}

var (
	// ErrNoRepo means the repository does not exist or the account may
	// not read it; the two cases are deliberately indistinguishable.
	ErrNoRepo = errors.New("sshd: no such repository")
	// ErrForbidden means the account may read but not write the repository.
	ErrForbidden = errors.New("sshd: forbidden")
)

// Authorizer decides access to repositories.
type Authorizer interface {
	// Authorize resolves owner/repo for the account and op and returns the
	// bare repository path on disk. ErrNoRepo for missing OR unreadable-private
	// repos (do not leak existence); ErrForbidden for writes without permission.
	Authorize(ctx context.Context, acct *Account, owner, repo string, op Op) (diskPath string, err error)
}

// Metrics receives observations; a nil Metrics is allowed.
type Metrics interface {
	// ObserveSession records one exec session with its operation
	// ("upload", "receive" or "invalid"), success and duration.
	ObserveSession(op string, success bool, d time.Duration)
	// ConnectionsChanged reports a change in the number of open connections.
	ConnectionsChanged(delta int)
}

// Config holds the server settings. Zero values take the defaults noted.
type Config struct {
	// MaxAuthTries bounds authentication attempts per connection (default 6).
	MaxAuthTries int
	// HandshakeTimeout bounds key exchange and authentication (default 20s).
	HandshakeTimeout time.Duration
	// SessionTimeout bounds a whole git operation (default 30m).
	SessionTimeout time.Duration
	// IdleTimeout closes connections with no traffic (default 5m). It is
	// also passed to upload-pack as --timeout.
	IdleTimeout time.Duration
	// MaxConns limits concurrent connections (default 256).
	MaxConns int
	// MaxConnsPerIP limits concurrent connections per source IP (default 16).
	MaxConnsPerIP int
	// HookSocket is exported to git subprocesses as FORGE_HOOK_SOCKET.
	HookSocket string
	// Banner, if set, is sent to clients before authentication.
	Banner string
}

// Permissions.Extensions keys set by the public-key callback.
const (
	extAccountID   = "forge.account-id"
	extAccountName = "forge.account-name"
	extFingerprint = "forge.key-fingerprint"
)

// serverVersion is announced in the protocol banner.
const serverVersion = "SSH-2.0-forge"

// maxSessionsPerConn bounds concurrently open session channels on one
// connection (OpenSSH ControlMaster multiplexing opens several).
const maxSessionsPerConn = 4

// Server is a Git-over-SSH server.
type Server struct {
	Config  Config
	Auth    Authenticator
	Authz   Authorizer
	Git     *git.Backend
	HostKey ssh.Signer
	// PreviousHostKeys are additionally offered during a host-key rotation
	// overlap so clients that pinned the old key keep connecting.
	PreviousHostKeys []ssh.Signer
	// Logger receives structured logs; nil uses slog.Default().
	Logger  *slog.Logger
	Metrics Metrics

	sshConfig  *ssh.ServerConfig
	listeners  []net.Listener
	conns      map[net.Conn]struct{}
	mu         sync.Mutex
	active     int
	perIP      map[string]int
	inShutdown atomic.Bool
	wg         sync.WaitGroup
	once       sync.Once
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Server) init() error {
	var err error
	s.once.Do(func() {
		if s.Auth == nil || s.Authz == nil || s.Git == nil || s.HostKey == nil {
			err = errors.New("sshd: Auth, Authz, Git and HostKey are required")
			return
		}
		c := &s.Config
		if c.MaxAuthTries == 0 {
			c.MaxAuthTries = 6
		}
		if c.HandshakeTimeout == 0 {
			c.HandshakeTimeout = 20 * time.Second
		}
		if c.SessionTimeout == 0 {
			c.SessionTimeout = 30 * time.Minute
		}
		if c.IdleTimeout == 0 {
			c.IdleTimeout = 5 * time.Minute
		}
		if c.MaxConns == 0 {
			c.MaxConns = 256
		}
		if c.MaxConnsPerIP == 0 {
			c.MaxConnsPerIP = 16
		}
		s.perIP = map[string]int{}
		s.conns = map[net.Conn]struct{}{}
		s.sshConfig = s.newSSHConfig()
	})
	return err
}

// newSSHConfig builds the hardened ssh.ServerConfig: public keys only, a
// modern algorithm allowlist, one ed25519 host key.
func (s *Server) newSSHConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		Config: ssh.Config{
			KeyExchanges: []string{
				ssh.KeyExchangeMLKEM768X25519,
				ssh.KeyExchangeCurve25519,
				"curve25519-sha256@libssh.org",
				ssh.KeyExchangeECDHP256,
				ssh.KeyExchangeECDHP384,
				ssh.KeyExchangeECDHP521,
			},
			Ciphers: []string{
				ssh.CipherChaCha20Poly1305,
				ssh.CipherAES256GCM,
				ssh.CipherAES128GCM,
				ssh.CipherAES256CTR,
				ssh.CipherAES128CTR,
			},
			MACs: []string{
				ssh.HMACSHA256ETM,
				ssh.HMACSHA512ETM,
				ssh.HMACSHA256,
				ssh.HMACSHA512,
			},
		},
		PublicKeyAuthAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoSKED25519,
			ssh.KeyAlgoSKECDSA256,
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoRSASHA512,
		},
		NoClientAuth:      false,
		MaxAuthTries:      s.Config.MaxAuthTries,
		ServerVersion:     serverVersion,
		PublicKeyCallback: s.publicKeyCallback,
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			if err != nil && method != "none" {
				s.logger().Debug("ssh auth failed", "remote", ipOf(conn.RemoteAddr()), "method", method, "err", err)
			}
		},
	}
	if s.Config.Banner != "" {
		banner := s.Config.Banner
		cfg.BannerCallback = func(ssh.ConnMetadata) string { return banner }
	}
	cfg.AddHostKey(s.HostKey)
	for _, k := range s.PreviousHostKeys {
		cfg.AddHostKey(k)
	}
	return cfg
}

// minRSABits is the smallest RSA modulus accepted for client keys.
const minRSABits = 2048

// publicKeyCallback resolves the account for an offered key. The login
// name is ignored. Every failure maps to the same error so a client cannot
// tell an unknown key from a revoked one.
func (s *Server) publicKeyCallback(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if _, isCert := key.(*ssh.Certificate); isCert {
		return nil, ErrUnknownKey
	}
	if ck, ok := key.(ssh.CryptoPublicKey); ok {
		if rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey); ok && rk.N.BitLen() < minRSABits {
			return nil, ErrUnknownKey
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Config.HandshakeTimeout)
	defer cancel()
	acct, err := s.Auth.AuthenticateKey(ctx, key)
	if err != nil {
		if !errors.Is(err, ErrUnknownKey) {
			s.logger().Error("authenticator error", "remote", ipOf(conn.RemoteAddr()), "err", err)
		}
		return nil, ErrUnknownKey
	}
	if acct == nil {
		return nil, ErrUnknownKey
	}
	return &ssh.Permissions{Extensions: map[string]string{
		extAccountID:   strconv.FormatInt(acct.ID, 10),
		extAccountName: acct.Name,
		extFingerprint: ssh.FingerprintSHA256(key),
	}}, nil
}

// accountFromPermissions recovers the account stored by publicKeyCallback.
// It reads only ssh.ServerConn.Permissions, i.e. the permissions of the
// key that actually completed authentication.
func accountFromPermissions(p *ssh.Permissions) (*Account, string, bool) {
	if p == nil || p.Extensions == nil {
		return nil, "", false
	}
	id, err := strconv.ParseInt(p.Extensions[extAccountID], 10, 64)
	if err != nil {
		return nil, "", false
	}
	return &Account{ID: id, Name: p.Extensions[extAccountName], Fingerprint: p.Extensions[extFingerprint]}, p.Extensions[extFingerprint], true
}

// Serve accepts connections on l until Shutdown or a fatal accept error.
func (s *Server) Serve(l net.Listener) error {
	if err := s.init(); err != nil {
		return err
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
	var delay time.Duration
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.inShutdown.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if delay == 0 {
					delay = 5 * time.Millisecond
				} else {
					delay *= 2
				}
				if delay > time.Second {
					delay = time.Second
				}
				time.Sleep(delay)
				continue
			}
			return err
		}
		delay = 0
		if !s.admit(conn) {
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.release(conn)
			s.handleConn(conn)
		}()
	}
}

// Shutdown closes listeners and waits for in-flight sessions up to ctx;
// when ctx expires the remaining connections are closed.
func (s *Server) Shutdown(ctx context.Context) error {
	s.inShutdown.Store(true)
	s.mu.Lock()
	for _, l := range s.listeners {
		_ = l.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		<-done
		return ctx.Err()
	}
}

func ipOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.String()
	}
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

func (s *Server) admit(c net.Conn) bool {
	ip := ipOf(c.RemoteAddr())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inShutdown.Load() || s.active >= s.Config.MaxConns || s.perIP[ip] >= s.Config.MaxConnsPerIP {
		s.logger().Debug("ssh connection refused: limit", "remote", ip)
		return false
	}
	s.active++
	s.perIP[ip]++
	s.conns[c] = struct{}{}
	if s.Metrics != nil {
		s.Metrics.ConnectionsChanged(1)
	}
	return true
}

func (s *Server) release(c net.Conn) {
	ip := ipOf(c.RemoteAddr())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	s.perIP[ip]--
	if s.perIP[ip] <= 0 {
		delete(s.perIP, ip)
	}
	delete(s.conns, c)
	if s.Metrics != nil {
		s.Metrics.ConnectionsChanged(-1)
	}
}

// handleConn runs the SSH handshake and the channel loop for one TCP
// connection.
func (s *Server) handleConn(nc net.Conn) {
	log := s.logger().With("remote", ipOf(nc.RemoteAddr()))
	ic := &idleConn{Conn: nc}
	defer ic.Close()

	// Bound the handshake with an absolute deadline; the idle wrapper stays
	// passive until authentication has completed.
	_ = nc.SetDeadline(time.Now().Add(s.Config.HandshakeTimeout))
	sconn, chans, reqs, err := ssh.NewServerConn(ic, s.sshConfig)
	if err != nil {
		log.Debug("ssh handshake failed", "err", err)
		return
	}
	_ = nc.SetDeadline(time.Time{})
	ic.setIdle(s.Config.IdleTimeout)

	acct, fp, ok := accountFromPermissions(sconn.Permissions)
	if !ok {
		// Cannot happen with NoClientAuth=false; refuse rather than guess.
		log.Error("ssh connection without account permissions")
		return
	}
	log = log.With("account", acct.Name, "key", fp)
	log.Debug("ssh connected", "client", string(sconn.ClientVersion()))

	// Global requests (tcpip-forward, keepalives, ...) are all refused.
	go ssh.DiscardRequests(reqs)

	var sessions sync.WaitGroup
	var open atomic.Int32
	for nch := range chans {
		if nch.ChannelType() != "session" {
			log.Info("ssh channel rejected", "type", nch.ChannelType())
			_ = nch.Reject(ssh.Prohibited, "forge: only session channels are permitted")
			continue
		}
		if open.Load() >= maxSessionsPerConn {
			_ = nch.Reject(ssh.ResourceShortage, "forge: too many sessions")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			log.Debug("ssh session accept failed", "err", err)
			continue
		}
		open.Add(1)
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer open.Add(-1)
			sess := &session{
				srv:    s,
				log:    log,
				acct:   acct,
				remote: ipOf(nc.RemoteAddr()),
				ch:     ch,
			}
			sess.run(chReqs)
		}()
	}
	sessions.Wait()
	_ = sconn.Wait()
}

// idleConn applies a rolling read/write deadline once idle > 0. Before
// that (during the handshake) it leaves the absolute deadline alone.
type idleConn struct {
	net.Conn
	idle atomic.Int64 // nanoseconds
}

func (c *idleConn) setIdle(d time.Duration) { c.idle.Store(int64(d)) }

func (c *idleConn) Read(p []byte) (int, error) {
	if d := c.idle.Load(); d > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(time.Duration(d)))
	}
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	if d := c.idle.Load(); d > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(time.Duration(d)))
	}
	return c.Conn.Write(p)
}
