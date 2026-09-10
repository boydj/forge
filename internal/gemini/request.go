package gemini

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
)

// Request is a parsed Gemini or Titan request.
type Request struct {
	// URL is the parsed request URL. Scheme is "gemini" or "titan".
	URL *url.URL
	// Raw is the request line without the trailing CRLF.
	Raw string
	// RemoteAddr is the peer address.
	RemoteAddr net.Addr
	// Certificate is the client certificate, if one was presented.
	Certificate *x509.Certificate
	// Fingerprint is the lowercase hex SHA-256 of the DER certificate, or "".
	Fingerprint string
	// Titan is non-nil for titan:// requests.
	Titan *TitanParams
	// Body reads exactly Titan.Size bytes for Titan requests; nil otherwise.
	Body io.Reader
	// ServerName is the SNI presented by the client.
	ServerName string
}

// TitanParams are the parameters of a Titan request line.
type TitanParams struct {
	Size  int64
	MIME  string
	Token string
}

// IsTitan reports whether the request is a Titan upload.
func (r *Request) IsTitan() bool { return r.Titan != nil }

// Path returns the URL path with a leading slash guaranteed.
func (r *Request) Path() string {
	p := r.URL.Path
	if p == "" {
		return "/"
	}
	return p
}

// Query returns the decoded query string (Gemini INPUT responses arrive here).
func (r *Request) Query() string {
	q, err := url.QueryUnescape(r.URL.RawQuery)
	if err != nil {
		return r.URL.RawQuery
	}
	return q
}

var (
	ErrRequestTooLong = errors.New("gemini: request too long")
	ErrBadRequest     = errors.New("gemini: malformed request")
)

// CertificateFingerprint returns the canonical fingerprint used as identity:
// lowercase hex SHA-256 over the DER encoding.
func CertificateFingerprint(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// parseRequestLine parses a request line (without CRLF) into a URL and, for
// Titan, its parameters. Titan parameters are appended to the last path
// segment as ";key=value" pairs, per the Titan specification.
func parseRequestLine(line string) (*url.URL, *TitanParams, error) {
	if line == "" || strings.ContainsAny(line, "\x00\r\n") {
		return nil, nil, ErrBadRequest
	}
	for _, c := range line {
		if c < 0x20 || c == 0x7f {
			return nil, nil, ErrBadRequest
		}
	}
	u, err := url.Parse(line)
	if err != nil {
		return nil, nil, ErrBadRequest
	}
	if u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return nil, nil, ErrBadRequest
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "gemini":
		if len(line) > MaxRequestBytes {
			return nil, nil, ErrRequestTooLong
		}
		if strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") || strings.Contains(u.Path, "//") {
			return nil, nil, ErrBadRequest
		}
		return u, nil, nil
	case "titan":
		if len(line) > MaxTitanRequestBytes {
			return nil, nil, ErrRequestTooLong
		}
		path, params, err := splitTitanParams(u.EscapedPath())
		if err != nil {
			return nil, nil, err
		}
		u.Path = path
		u.RawPath = ""
		if strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") || strings.Contains(u.Path, "//") {
			return nil, nil, ErrBadRequest
		}
		return u, params, nil
	default:
		return nil, nil, ErrBadRequest
	}
}

// splitTitanParams separates ";size=N;mime=T;token=X" from the escaped path.
// The parameters must appear after the final path segment; "size" is
// required. The returned path is unescaped.
func splitTitanParams(escaped string) (string, *TitanParams, error) {
	i := strings.Index(escaped, ";")
	if i < 0 {
		return "", nil, ErrBadRequest
	}
	rawBase, rest := escaped[:i], escaped[i+1:]
	base, err := url.PathUnescape(rawBase)
	if err != nil || strings.Contains(base, ";") {
		return "", nil, ErrBadRequest
	}
	p := &TitanParams{MIME: "text/gemini"}
	seenSize := false
	for _, kv := range strings.Split(rest, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return "", nil, ErrBadRequest
		}
		switch k {
		case "size":
			if seenSize {
				return "", nil, ErrBadRequest
			}
			seenSize = true
			var n int64
			if v == "" || len(v) > 15 {
				return "", nil, ErrBadRequest
			}
			for _, c := range v {
				if c < '0' || c > '9' {
					return "", nil, ErrBadRequest
				}
				n = n*10 + int64(c-'0')
			}
			p.Size = n
		case "mime":
			mv, err := url.PathUnescape(v)
			if err != nil || mv == "" || len(mv) > 255 {
				return "", nil, ErrBadRequest
			}
			p.MIME = mv
		case "token":
			tv, err := url.PathUnescape(v)
			if err != nil || len(tv) > 255 {
				return "", nil, ErrBadRequest
			}
			p.Token = tv
		default:
			return "", nil, ErrBadRequest
		}
	}
	if !seenSize {
		return "", nil, ErrBadRequest
	}
	return base, p, nil
}
