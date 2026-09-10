package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is an account.
type User struct {
	ID          int64
	Name        string
	DisplayName string
	Bio         string
	Admin       bool
	Disabled    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Certificate is a registered TLS client certificate.
type Certificate struct {
	ID         int64
	UserID     int64
	SPKISHA256 string
	CertSHA256 string
	Subject    string
	Label      string
	NotBefore  time.Time
	NotAfter   time.Time
	CreatedAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
}

// SSHKey is a registered SSH public key.
type SSHKey struct {
	ID          int64
	UserID      int64
	Fingerprint string
	KeyType     string
	PublicKey   string
	Label       string
	CreatedAt   time.Time
	LastUsedAt  time.Time
	RevokedAt   time.Time
}

const userCols = `id, name, display_name, bio, admin, disabled, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var c, up string
	if err := row.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Bio, &u.Admin, &u.Disabled, &c, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt, u.UpdatedAt = ParseTime(c), ParseTime(up)
	return &u, nil
}

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, name string, admin bool) (*User, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (name, admin, created_at, updated_at) VALUES (?, ?, ?, ?)`, name, admin, now, now)
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(ctx, id)
}

// UserByID loads a user.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// UserByName loads a user by name.
func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name = ?`, name))
}

// UpdateUserProfile sets display name and bio.
func (s *Store) UpdateUserProfile(ctx context.Context, id int64, display, bio string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET display_name = ?, bio = ?, updated_at = ? WHERE id = ?`, display, bio, Now(), id)
	return err
}

// SetUserDisabled enables or disables an account.
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, disabled, Now(), id)
	return err
}

// SetUserAdmin grants or revokes admin.
func (s *Store) SetUserAdmin(ctx context.Context, id int64, admin bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET admin = ?, updated_at = ? WHERE id = ?`, admin, Now(), id)
	return err
}

// ListUsers lists users by name.
func (s *Store) ListUsers(ctx context.Context, limit, offset int) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

const certCols = `id, user_id, spki_sha256, cert_sha256, subject, label, not_before, not_after, created_at, last_used_at, revoked_at`

func scanCert(row interface{ Scan(...any) error }) (*Certificate, error) {
	var c Certificate
	var nb, na, lu, rv sql.NullString
	var cr string
	if err := row.Scan(&c.ID, &c.UserID, &c.SPKISHA256, &c.CertSHA256, &c.Subject, &c.Label, &nb, &na, &cr, &lu, &rv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.NotBefore, c.NotAfter, c.CreatedAt = nullTime(nb), nullTime(na), ParseTime(cr)
	c.LastUsedAt, c.RevokedAt = nullTime(lu), nullTime(rv)
	return &c, nil
}

// AddCertificate registers a certificate for a user.
func (s *Store) AddCertificate(ctx context.Context, c *Certificate) (*Certificate, error) {
	var nb, na any
	if !c.NotBefore.IsZero() {
		nb = c.NotBefore.UTC().Format(time.RFC3339)
	}
	if !c.NotAfter.IsZero() {
		na = c.NotAfter.UTC().Format(time.RFC3339)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO certificates (user_id, spki_sha256, cert_sha256, subject, label, not_before, not_after, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UserID, c.SPKISHA256, c.CertSHA256, c.Subject, c.Label, nb, na, Now())
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return scanCert(s.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certificates WHERE id = ?`, id))
}

// CertificateBySPKI finds a certificate by public-key hash (any state).
func (s *Store) CertificateBySPKI(ctx context.Context, spki string) (*Certificate, error) {
	return scanCert(s.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certificates WHERE spki_sha256 = ?`, spki))
}

// TouchCertificate records use and updates the certificate hash if the user
// re-issued a certificate for the same key.
func (s *Store) TouchCertificate(ctx context.Context, id int64, certSHA string, notAfter time.Time) error {
	var na any
	if !notAfter.IsZero() {
		na = notAfter.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE certificates SET last_used_at = ?, cert_sha256 = ?, not_after = ? WHERE id = ?`, Now(), certSHA, na, id)
	return err
}

// RevokeCertificate marks a certificate revoked.
func (s *Store) RevokeCertificate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE certificates SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, Now(), id)
	return err
}

// ListCertificates lists a user's certificates.
func (s *Store) ListCertificates(ctx context.Context, userID int64) ([]*Certificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+certCols+` FROM certificates WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Certificate
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const keyCols = `id, user_id, fingerprint, key_type, public_key, label, created_at, last_used_at, revoked_at`

func scanKey(row interface{ Scan(...any) error }) (*SSHKey, error) {
	var k SSHKey
	var cr string
	var lu, rv sql.NullString
	if err := row.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.KeyType, &k.PublicKey, &k.Label, &cr, &lu, &rv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	k.CreatedAt, k.LastUsedAt, k.RevokedAt = ParseTime(cr), nullTime(lu), nullTime(rv)
	return &k, nil
}

// AddSSHKey registers a key.
func (s *Store) AddSSHKey(ctx context.Context, k *SSHKey) (*SSHKey, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (user_id, fingerprint, key_type, public_key, label, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		k.UserID, k.Fingerprint, k.KeyType, k.PublicKey, k.Label, Now())
	if err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE id = ?`, id))
}

// SSHKeyByFingerprint finds a key (any state).
func (s *Store) SSHKeyByFingerprint(ctx context.Context, fp string) (*SSHKey, error) {
	return scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE fingerprint = ?`, fp))
}

// TouchSSHKey records use.
func (s *Store) TouchSSHKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ssh_keys SET last_used_at = ? WHERE id = ?`, Now(), id)
	return err
}

// RevokeSSHKey marks a key revoked.
func (s *Store) RevokeSSHKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ssh_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, Now(), id)
	return err
}

// DeleteSSHKey removes a key.
func (s *Store) DeleteSSHKey(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSSHKeys lists a user's keys.
func (s *Store) ListSSHKeys(ctx context.Context, userID int64) ([]*SSHKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SSHKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Token is a one-time code.
type Token struct {
	ID        int64
	UserID    int64
	Kind      string
	Scope     string
	ExpiresAt time.Time
	UsedAt    time.Time
}

// CreateToken stores a hashed token.
func (s *Store) CreateToken(ctx context.Context, userID int64, kind, hash, scope string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (user_id, kind, token_hash, scope, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, kind, hash, scope, expires.UTC().Format(time.RFC3339), Now())
	return err
}

// ConsumeToken atomically marks a valid, unexpired, unused token as used and
// returns it; ErrNotFound otherwise.
func (s *Store) ConsumeToken(ctx context.Context, kind, hash string) (*Token, error) {
	var t Token
	var exp string
	err := s.db.QueryRowContext(ctx, `UPDATE tokens SET used_at = ? WHERE kind = ? AND token_hash = ? AND used_at IS NULL AND expires_at > ? RETURNING id, user_id, kind, scope, expires_at`,
		Now(), kind, hash, Now()).Scan(&t.ID, &t.UserID, &t.Kind, &t.Scope, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.ExpiresAt = ParseTime(exp)
	return &t, nil
}

// PurgeTokens deletes expired or used tokens.
func (s *Store) PurgeTokens(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE used_at IS NOT NULL OR expires_at < ?`, Now())
	return err
}
