package gemini

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ResponseWriter writes a Gemini response. Exactly one header is written; a
// body may follow only for 2x statuses.
type ResponseWriter interface {
	// Header writes the status line. It may be called once. For non-2x
	// statuses META carries the redirect target, prompt or error message.
	Header(status int, meta string) error
	// Write appends body bytes. It fails if no 2x header was written.
	Write(p []byte) (int, error)
	// Status returns the status written, or 0.
	Status() int
}

type responseWriter struct {
	w       *bufio.Writer
	status  int
	written int64
}

func newResponseWriter(w io.Writer) *responseWriter {
	return &responseWriter{w: bufio.NewWriterSize(w, 32*1024)}
}

// SanitizeMeta strips CR/LF and control characters from META and truncates it
// to the wire limit so user-supplied strings can never inject a second
// response line.
func SanitizeMeta(meta string) string {
	var b strings.Builder
	for _, r := range meta {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) > MaxMetaBytes {
		s = s[:MaxMetaBytes]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func (r *responseWriter) Header(status int, meta string) error {
	if r.status != 0 {
		return errors.New("gemini: header already written")
	}
	if status < 10 || status > 69 {
		return fmt.Errorf("gemini: invalid status %d", status)
	}
	r.status = status
	_, err := fmt.Fprintf(r.w, "%d %s\r\n", status, SanitizeMeta(meta))
	return err
}

func (r *responseWriter) Write(p []byte) (int, error) {
	if r.status < 20 || r.status > 29 {
		return 0, errors.New("gemini: body without success header")
	}
	n, err := r.w.Write(p)
	r.written += int64(n)
	return n, err
}

func (r *responseWriter) Status() int { return r.status }

func (r *responseWriter) flush() error { return r.w.Flush() }

// Helpers for handlers.

// Gemtext writes a 20 text/gemini header.
func Gemtext(w ResponseWriter) error { return w.Header(StatusSuccess, "text/gemini; charset=utf-8") }

// Input asks the client for a line of input.
func Input(w ResponseWriter, prompt string) error { return w.Header(StatusInput, prompt) }

// Redirect sends a temporary redirect.
func Redirect(w ResponseWriter, target string) error {
	return w.Header(StatusRedirectTemporary, target)
}

// NotFound sends 51.
func NotFound(w ResponseWriter) error { return w.Header(StatusNotFound, "not found") }

// BadRequest sends 59.
func BadRequest(w ResponseWriter, msg string) error { return w.Header(StatusBadRequest, msg) }

// CertRequired sends 60.
func CertRequired(w ResponseWriter, msg string) error {
	return w.Header(StatusCertificateRequired, msg)
}

// Forbidden sends 61.
func Forbidden(w ResponseWriter, msg string) error {
	return w.Header(StatusCertificateNotAuth, msg)
}

// TemporaryFailure sends 40.
func TemporaryFailure(w ResponseWriter, msg string) error {
	return w.Header(StatusTemporaryFailure, msg)
}

// PermanentFailure sends 50.
func PermanentFailure(w ResponseWriter, msg string) error {
	return w.Header(StatusPermanentFailure, msg)
}
