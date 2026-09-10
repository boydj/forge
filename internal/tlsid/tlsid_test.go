package tlsid

import (
	"crypto/x509"
	"path/filepath"
	"testing"
)

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "s.crt"), filepath.Join(dir, "s.key")
	c, created, err := LoadOrCreate(cf, kf, []string{"git.example.org", "127.0.0.1"})
	if err != nil || !created {
		t.Fatal(err, created)
	}
	x, _ := x509.ParseCertificate(c.Certificate[0])
	if x.DNSNames[0] != "git.example.org" || len(x.IPAddresses) != 1 {
		t.Errorf("SAN: %v %v", x.DNSNames, x.IPAddresses)
	}
	c2, created, err := LoadOrCreate(cf, kf, nil)
	if err != nil || created || Fingerprint(c2) != Fingerprint(c) {
		t.Errorf("reload: %v %v", err, created)
	}
}
