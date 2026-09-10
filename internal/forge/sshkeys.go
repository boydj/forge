package forge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/store"
)

// allowedKeyTypes are accepted SSH key algorithms.
var allowedKeyTypes = map[string]bool{
	ssh.KeyAlgoED25519:                 true,
	ssh.KeyAlgoECDSA256:                true,
	ssh.KeyAlgoECDSA384:                true,
	ssh.KeyAlgoECDSA521:                true,
	ssh.KeyAlgoRSA:                     true,
	ssh.KeyAlgoSKED25519:               true,
	ssh.KeyAlgoSKECDSA256:              true,
	"ssh-ed25519-cert-v01@openssh.com": false,
}

// ParseAuthorizedKeys parses authorized_keys text into keys, ignoring
// comments and blank lines. Each key's comment becomes its label.
func ParseAuthorizedKeys(text string) ([]*store.SSHKey, error) {
	var keys []*store.SSHKey
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	line := 0
	for sc.Scan() {
		line++
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(l))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, ErrNotAcceptable)
		}
		if !allowedKeyTypes[pub.Type()] {
			return nil, fmt.Errorf("line %d: key type %s: %w", line, pub.Type(), ErrNotAcceptable)
		}
		if ck, ok := pub.(ssh.CryptoPublicKey); ok {
			if rsaKey, ok := ck.CryptoPublicKey().(interface{ Size() int }); ok && rsaKey.Size() < 256 {
				return nil, fmt.Errorf("line %d: RSA key shorter than 2048 bits: %w", line, ErrNotAcceptable)
			}
		}
		label := comment
		if len(label) > 64 {
			label = label[:64]
		}
		keys = append(keys, &store.SSHKey{
			Fingerprint: ssh.FingerprintSHA256(pub),
			KeyType:     pub.Type(),
			PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
			Label:       label,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, ErrTooLarge
	}
	return keys, nil
}

// AddSSHKeys registers keys for a user. Duplicates already owned by the same
// user are skipped; keys owned by another account are rejected.
func (f *Forge) AddSSHKeys(ctx context.Context, u *store.User, text string) ([]*store.SSHKey, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	keys, err := ParseAuthorizedKeys(text)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrNotAcceptable
	}
	existing, err := f.Store.ListSSHKeys(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if len(existing)+len(keys) > 32 {
		return nil, ErrQuota
	}
	var added []*store.SSHKey
	for _, k := range keys {
		k.UserID = u.ID
		if cur, err := f.Store.SSHKeyByFingerprint(ctx, k.Fingerprint); err == nil {
			if cur.UserID != u.ID {
				return added, fmt.Errorf("key %s is registered to another account: %w", k.Fingerprint, ErrExists)
			}
			continue
		}
		stored, err := f.Store.AddSSHKey(ctx, k)
		if err != nil {
			return added, err
		}
		added = append(added, stored)
	}
	if len(added) > 0 {
		f.Event(ctx, store.EventUserKeyAdd, nil, u, "SSH key added", "/account/keys", nil)
	}
	return added, nil
}

// RemoveSSHKey deletes a user's key by fingerprint.
func (f *Forge) RemoveSSHKey(ctx context.Context, u *store.User, fingerprint string) error {
	k, err := f.Store.SSHKeyByFingerprint(ctx, fingerprint)
	if errors.Is(err, store.ErrNotFound) || (err == nil && k.UserID != u.ID) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return f.Store.DeleteSSHKey(ctx, u.ID, k.ID)
}

// UserForSSHKey resolves an SSH public key to an active account.
func (f *Forge) UserForSSHKey(ctx context.Context, pub ssh.PublicKey) (*store.User, *store.SSHKey, error) {
	k, err := f.Store.SSHKeyByFingerprint(ctx, ssh.FingerprintSHA256(pub))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if !k.RevokedAt.IsZero() {
		return nil, nil, ErrForbidden
	}
	// Compare the full key, not only the fingerprint.
	if k.PublicKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) {
		return nil, nil, ErrNotFound
	}
	u, err := f.Store.UserByID(ctx, k.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrDisabled
	}
	return u, k, nil
}
