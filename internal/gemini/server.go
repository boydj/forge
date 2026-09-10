package gemini

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Handler serves a single Gemini or Titan request.
type Handler interface {
	ServeGemini(ctx context.Context, w ResponseWriter, r *Request)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, w ResponseWriter, r *Request)

// ServeGemini implements Handler.
func (f HandlerFunc) ServeGemini(ctx context.Context, w ResponseWriter, r *Request) { f(ctx, w, r) }

// Server is a Gemini/Titan server.
type Server struct {
	Handler Handler
	// TLSConfig must contain the server certificate. ClientAuth is forced to
	// RequestClientCert and MinVersion to TLS 1.2.
	TLSConfig *tls.Config
	// ReadTimeout bounds reading the request line (default 10s).
	ReadTimeout time.Duration
	// WriteTimeout bounds writing the response (default 60s).
	WriteTimeout time.Duration
	// TitanBodyTimeout bounds reading a Titan body (default 120s).
	TitanBodyTimeout time.Duration
	// MaxTitanBody rejects Titan requests larger than this before any body is
	// read (default 16 MiB). Handlers apply tighter per-path limits.
	MaxTitanBody int64
	// MaxConns limits concurrent connections (default 1024).
	MaxConns int
	// MaxConnsPerIP limits concurrent connections per source address (default 32).
	MaxConnsPerIP int
	// Logger receives structured logs; nil uses slog.Default().
	Logger *slog.Logger
	// Metrics receives per-request observations; may be nil.
	Metrics Metrics

	listeners  []net.Listener
	mu         sync.Mutex
	active     int64
	perIP      map[string]int
	inShutdown atomic.Bool
	wg         sync.WaitGroup
}

// Metrics is implemented by the metrics package to observe requests.
type Metrics interface {
	ObserveRequest(scheme string, status int, d time.Duration, bodyBytes int64)
	ConnectionsChanged(delta int)
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Server) defaults() {
	if s.ReadTimeout == 0 {
		s.ReadTimeout = 10 * time.Second
	}
	if s.WriteTimeout == 0 {
		s.WriteTimeout = 60 * time.Second
	}
	if s.TitanBodyTimeout == 0 {
		s.TitanBodyTimeout = 120 * time.Second
	}
	if s.MaxTitanBody == 0 {
		s.MaxTitanBody = 16 << 20
	}
	if s.MaxConns == 0 {
		s.MaxConns = 1024
	}
	if s.MaxConnsPerIP == 0 {
		s.MaxConnsPerIP = 32
	}
	if s.perIP == nil {
		s.perIP = map[string]int{}
	}
}

// TLSServerConfig returns a tls.Config suitable for Gemini with the given
// certificate: TLS 1.2 minimum, client certificates requested but not
// verified against any CA (identity is by fingerprint).
func TLSServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequestClientCert,
	}
}

// Serve accepts connections on l until Shutdown or a fatal accept error.
func (s *Server) Serve(l net.Listener) error {
	s.defaults()
	cfg := s.TLSConfig.Clone()
	cfg.ClientAuth = tls.RequestClientCert
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
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
			s.handleConn(tls.Server(conn, cfg))
		}()
	}
}

func ipOf(a net.Addr) string {
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
	if int(s.active) >= s.MaxConns || s.perIP[ip] >= s.MaxConnsPerIP {
		return false
	}
	s.active++
	s.perIP[ip]++
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
	if s.Metrics != nil {
		s.Metrics.ConnectionsChanged(-1)
	}
}

// Shutdown closes listeners and waits for in-flight requests up to ctx.
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
		return ctx.Err()
	}
}

func (s *Server) handleConn(conn *tls.Conn) {
	defer conn.Close()
	start := time.Now()
	log := s.logger()
	_ = conn.SetDeadline(time.Now().Add(s.ReadTimeout))
	if err := conn.Handshake(); err != nil {
		log.Debug("tls handshake failed", "remote", conn.RemoteAddr(), "err", err)
		return
	}
	br := bufio.NewReaderSize(conn, MaxTitanRequestBytes+2)
	line, err := readRequestLine(br)
	rw := newResponseWriter(conn)
	if err != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		if errors.Is(err, ErrRequestTooLong) {
			_ = rw.Header(StatusBadRequest, "request too long")
		} else {
			_ = rw.Header(StatusBadRequest, "malformed request")
		}
		_ = rw.flush()
		return
	}
	u, titan, err := parseRequestLine(line)
	if err != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		_ = rw.Header(StatusBadRequest, "malformed request")
		_ = rw.flush()
		return
	}
	state := conn.ConnectionState()
	req := &Request{URL: u, Raw: line, RemoteAddr: conn.RemoteAddr(), ServerName: state.ServerName, Titan: titan}
	if len(state.PeerCertificates) > 0 {
		req.Certificate = state.PeerCertificates[0]
		req.Fingerprint = CertificateFingerprint(req.Certificate)
	}
	var bodyBytes int64
	if titan != nil {
		if titan.Size > s.MaxTitanBody {
			_ = conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
			_ = rw.Header(StatusBadRequest, "upload too large")
			_ = rw.flush()
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(s.TitanBodyTimeout))
		cr := &countingReader{R: io.LimitReader(br, titan.Size)}
		req.Body = cr
		defer func() { bodyBytes = cr.N }()
	}
	_ = conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
	ctx, cancel := context.WithTimeout(context.Background(), s.WriteTimeout)
	defer cancel()
	func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("handler panic", "path", u.Path, "panic", p)
				if rw.Status() == 0 {
					_ = rw.Header(StatusTemporaryFailure, "internal error")
				}
			}
		}()
		s.Handler.ServeGemini(ctx, rw, req)
	}()
	if rw.Status() == 0 {
		_ = rw.Header(StatusTemporaryFailure, "no response")
	}
	if err := rw.flush(); err != nil {
		log.Debug("write failed", "remote", conn.RemoteAddr(), "err", err)
	}
	// Drain a bounded amount of an unread Titan body so that an early
	// rejection is not turned into a TCP reset before the client has read
	// the status line.
	if cr, ok := req.Body.(*countingReader); ok && titan != nil && cr.N < titan.Size {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.CopyN(io.Discard, cr, min(titan.Size-cr.N, 256<<10))
	}
	// Send close_notify so strict clients see a clean EOF.
	_ = conn.CloseWrite()
	d := time.Since(start)
	if s.Metrics != nil {
		s.Metrics.ObserveRequest(u.Scheme, rw.Status(), d, bodyBytes)
	}
	log.Info("request",
		"scheme", u.Scheme, "path", u.Path, "status", rw.Status(),
		"bytes", rw.written, "ms", d.Milliseconds(),
		"remote", ipOf(conn.RemoteAddr()), "cert", req.Fingerprint != "")
}

type countingReader struct {
	R io.Reader
	N int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	c.N += int64(n)
	return n, err
}

// readRequestLine reads up to MaxTitanRequestBytes+2 bytes terminated by CRLF.
func readRequestLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", ErrBadRequest
		}
		buf = append(buf, b)
		if len(buf) > MaxTitanRequestBytes+2 {
			return "", ErrRequestTooLong
		}
		if b == '\n' {
			break
		}
	}
	n := len(buf)
	if n < 2 || buf[n-2] != '\r' {
		return "", ErrBadRequest
	}
	return string(buf[:n-2]), nil
}
