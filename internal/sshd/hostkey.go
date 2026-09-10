package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// LoadOrCreateHostKey loads the ed25519 host key at path (OpenSSH or PEM
// private key format, unencrypted). If the file does not exist a new
// ed25519 key is generated, written with mode 0600 in OpenSSH format, and
// its public half is written beside it as path+".pub". Keys of any other
// type are refused: the forge presents exactly one ed25519 host key.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if err := generateHostKey(path); err != nil {
			return nil, err
		}
		if data, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("sshd: host key: %w", err)
		}
	default:
		return nil, fmt.Errorf("sshd: host key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("sshd: host key %s: %w", path, err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("sshd: host key %s: %s is not an ed25519 key", path, signer.PublicKey().Type())
	}
	return signer, nil
}

func generateHostKey(path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "forge host key")
	if err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	if err := pem.Encode(f, block); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		return fmt.Errorf("sshd: generate host key: %w", err)
	}
	return nil
}

// SSHFPRecords returns the RFC 4255 / RFC 6594 SSHFP RDATA strings for pub:
// one with a SHA-1 fingerprint ("<alg> 1 <hex>") and one with SHA-256
// ("<alg> 2 <hex>"). The algorithm number is 1 for RSA, 2 for DSA, 3 for
// ECDSA and 4 for Ed25519; nil is returned for key types without an
// assigned number (FIDO/sk keys, certificates).
func SSHFPRecords(pub ssh.PublicKey) []string {
	var alg int
	switch t := pub.Type(); {
	case t == ssh.KeyAlgoRSA:
		alg = 1
	case t == "ssh-dss":
		alg = 2 // DSA is never generated here; listed for completeness
	case strings.HasPrefix(t, "ecdsa-sha2-"):
		alg = 3
	case t == ssh.KeyAlgoED25519:
		alg = 4
	default:
		return nil
	}
	blob := pub.Marshal()
	s1 := sha1.Sum(blob)
	s256 := sha256.Sum256(blob)
	return []string{
		fmt.Sprintf("%d 1 %s", alg, hex.EncodeToString(s1[:])),
		fmt.Sprintf("%d 2 %s", alg, hex.EncodeToString(s256[:])),
	}
}
