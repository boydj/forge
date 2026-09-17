// Package tlsid manages the service TLS identity: a long-lived self-signed
// certificate, as Gemini clients expect (TOFU), stored as PEM files.
package tlsid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Lifetime of generated certificates. Gemini practice is long-lived
// certificates; rotation is an explicit operator action (see docs/tls.md).
const Lifetime = 10 * 365 * 24 * time.Hour

// LoadOrCreate loads the certificate/key pair or generates a self-signed
// ECDSA P-256 certificate for the given names (DNS names or IP addresses).
func LoadOrCreate(certFile, keyFile string, names []string) (tls.Certificate, bool, error) {
	if _, err := os.Stat(keyFile); err == nil {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		return c, false, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, false, err
	}
	if len(names) == 0 {
		return tls.Certificate{}, false, errors.New("tlsid: at least one hostname is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, false, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(Lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o750); err != nil {
		return tls.Certificate{}, false, err
	}
	if err := writeFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, false, err
	}
	if err := writeFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, false, err
	}
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	return c, true, err
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fingerprint returns the SHA-256 fingerprint of the leaf certificate.
func Fingerprint(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(c.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// Describe returns a one-line summary for logs and status pages.
func Describe(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return "no certificate"
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return "unparseable certificate"
	}
	return fmt.Sprintf("CN=%s SAN=%v expires %s sha256=%s", x.Subject.CommonName, x.DNSNames, x.NotAfter.Format("2006-01-02"), Fingerprint(c))
}
