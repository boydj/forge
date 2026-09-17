// Package gemini implements the Gemini protocol server side, plus the Titan
// upload extension, on top of crypto/tls.
//
// It deliberately contains no forge-specific logic: it parses requests,
// exposes the client certificate, writes responses and enforces the wire
// limits from the specification.
package gemini

// Status codes from the Gemini protocol specification.
const (
	StatusInput               = 10
	StatusSensitiveInput      = 11
	StatusSuccess             = 20
	StatusRedirectTemporary   = 30
	StatusRedirectPermanent   = 31
	StatusTemporaryFailure    = 40
	StatusServerUnavailable   = 41
	StatusCGIError            = 42
	StatusProxyError          = 43
	StatusSlowDown            = 44
	StatusPermanentFailure    = 50
	StatusNotFound            = 51
	StatusGone                = 52
	StatusProxyRequestRefused = 53
	StatusBadRequest          = 59
	StatusCertificateRequired = 60
	StatusCertificateNotAuth  = 61
	StatusCertificateInvalid  = 62
)

// MaxRequestBytes is the maximum size of a Gemini request URL. The wire limit
// is 1024 bytes of URL plus CRLF; Titan request lines carry parameters after
// the path and are allowed to be longer (see MaxTitanRequestBytes).
const MaxRequestBytes = 1024

// MaxTitanRequestBytes bounds a Titan request line (URL plus ;size=;mime=;token=).
const MaxTitanRequestBytes = 2048

// MaxMetaBytes is the maximum size of the META field in a response header.
const MaxMetaBytes = 1024
