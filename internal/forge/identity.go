package forge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"as215520.net/forge/internal/store"
)

// Identity is the result of authenticating a request.
type Identity struct {
	// User is set when the certificate maps to an active account.
	User *store.User
	Cert *store.Certificate
	// SPKI is the public-key hash of the presented certificate (set even
	// when unknown), "" when no certificate was presented.
	SPKI string
	// CertSHA256 is the certificate hash.
	CertSHA256 string
	// Subject is the certificate subject common name (informational).
	Subject  string
	NotAfter time.Time
}

// Anonymous reports whether no account is bound.
func (i *Identity) Anonymous() bool { return i == nil || i.User == nil }

// UserID returns the account id or 0.
func (i *Identity) UserID() int64 {
	if i == nil || i.User == nil {
		return 0
	}
	return i.User.ID
}

// IsAdmin reports whether the identity is a forge administrator.
func (i *Identity) IsAdmin() bool { return i != nil && i.User != nil && i.User.Admin }

// SPKIFingerprint hashes the SubjectPublicKeyInfo of a certificate.
func SPKIFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// Authenticate maps a client certificate to an identity. With no certificate
// it returns an empty identity and nil error. With an unregistered
// certificate it returns an identity with SPKI set and ErrCertUnknown so
// callers can offer registration. Revoked/expired certificates and disabled
// accounts return the respective error.
func (f *Forge) Authenticate(ctx context.Context, cert *x509.Certificate) (*Identity, error) {
	if cert == nil {
		return &Identity{}, nil
	}
	id := &Identity{SPKI: SPKIFingerprint(cert), Subject: cert.Subject.CommonName, NotAfter: cert.NotAfter}
	sum := sha256.Sum256(cert.Raw)
	id.CertSHA256 = hex.EncodeToString(sum[:])
	now := time.Now()
	if !cert.NotAfter.IsZero() && now.After(cert.NotAfter) {
		return id, ErrCertExpired
	}
	if !cert.NotBefore.IsZero() && now.Before(cert.NotBefore.Add(-24*time.Hour)) {
		return id, ErrCertExpired
	}
	c, err := f.Store.CertificateBySPKI(ctx, id.SPKI)
	if errors.Is(err, store.ErrNotFound) {
		return id, ErrCertUnknown
	}
	if err != nil {
		return id, err
	}
	if !c.RevokedAt.IsZero() {
		return id, ErrCertRevoked
	}
	u, err := f.Store.UserByID(ctx, c.UserID)
	if err != nil {
		return id, err
	}
	if u.Disabled {
		return id, ErrDisabled
	}
	id.User, id.Cert = u, c
	if c.CertSHA256 != id.CertSHA256 || c.LastUsedAt.Before(now.Add(-time.Hour)) {
		_ = f.Store.TouchCertificate(ctx, c.ID, id.CertSHA256, cert.NotAfter)
	}
	return id, nil
}

// Register creates an account bound to the presented certificate.
func (f *Forge) Register(ctx context.Context, id *Identity, name string) (*store.User, error) {
	if id == nil || id.SPKI == "" {
		return nil, ErrAuthRequired
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if err := ValidUserName(name); err != nil {
		return nil, err
	}
	if _, err := f.Store.CertificateBySPKI(ctx, id.SPKI); err == nil {
		return nil, ErrExists
	}
	n, err := f.Store.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	// The first account on a fresh forge becomes the administrator.
	u, err := f.Store.CreateUser(ctx, name, n == 0)
	if errors.Is(err, store.ErrConflict) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.AddCertificate(ctx, u, id, "first"); err != nil {
		return nil, err
	}
	f.Event(ctx, store.EventUserCreate, nil, u, "new user "+u.Name, "/~"+u.Name+"/", nil)
	return u, nil
}

// AddCertificate binds the presented certificate to an account.
func (f *Forge) AddCertificate(ctx context.Context, u *store.User, id *Identity, label string) (*store.Certificate, error) {
	if id == nil || id.SPKI == "" {
		return nil, ErrAuthRequired
	}
	c, err := f.Store.AddCertificate(ctx, &store.Certificate{
		UserID: u.ID, SPKISHA256: id.SPKI, CertSHA256: id.CertSHA256, Subject: id.Subject,
		Label: label, NotAfter: id.NotAfter,
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	f.Event(ctx, store.EventUserCertAdd, nil, u, u.Name+" added a certificate", "/~"+u.Name+"/", nil)
	return c, nil
}

// EnrolmentTokenTTL bounds one-time certificate enrolment codes.
const EnrolmentTokenTTL = 15 * time.Minute

// HashToken hashes a one-time code for storage.
func HashToken(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

// NewEnrolmentCode creates a one-time code allowing another certificate to
// be added to the account.
func NewEnrolmentCode(ctx context.Context, f *Forge, u *store.User) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b[:])
	if err := f.Store.CreateToken(ctx, u.ID, "enrol", HashToken(code), "", time.Now().Add(EnrolmentTokenTTL)); err != nil {
		return "", err
	}
	return code, nil
}
