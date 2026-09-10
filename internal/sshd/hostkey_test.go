package sshd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLoadOrCreateHostKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh", "host_ed25519")
	signer, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("type %s", signer.PublicKey().Type())
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("private key mode %o", st.Mode().Perm())
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != string(ssh.MarshalAuthorizedKey(signer.PublicKey())) {
		t.Errorf(".pub mismatch: %q", pub)
	}
	priv, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(priv), "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Errorf("not OpenSSH format: %.40q", priv)
	}
	// Second load returns the same key.
	again, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(again.PublicKey()) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Error("reload produced a different key")
	}
	// Non-ed25519 keys are refused.
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	block, _ := ssh.MarshalPrivateKey(rk, "")
	rsaPath := filepath.Join(dir, "rsa")
	_ = os.WriteFile(rsaPath, pem.EncodeToMemory(block), 0o600)
	if _, err := LoadOrCreateHostKey(rsaPath); err == nil || !strings.Contains(err.Error(), "not an ed25519") {
		t.Errorf("rsa host key accepted: %v", err)
	}
	// Garbage is refused.
	badPath := filepath.Join(dir, "bad")
	_ = os.WriteFile(badPath, []byte("nope"), 0o600)
	if _, err := LoadOrCreateHostKey(badPath); err == nil {
		t.Error("garbage host key accepted")
	}
}

func TestSSHFPRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	signer, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	recs := SSHFPRecords(signer.PublicKey())
	if len(recs) != 2 || !strings.HasPrefix(recs[0], "4 1 ") || !strings.HasPrefix(recs[1], "4 2 ") {
		t.Fatalf("records: %v", recs)
	}
	if len(recs[0]) != len("4 1 ")+40 || len(recs[1]) != len("4 2 ")+64 {
		t.Errorf("hex lengths: %v", recs)
	}
	// Algorithm numbers for other key types.
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rpub, _ := ssh.NewPublicKey(&rk.PublicKey)
	if r := SSHFPRecords(rpub); len(r) != 2 || !strings.HasPrefix(r[0], "1 1 ") {
		t.Errorf("rsa: %v", r)
	}
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	epub, _ := ssh.NewPublicKey(&ek.PublicKey)
	if r := SSHFPRecords(epub); len(r) != 2 || !strings.HasPrefix(r[0], "3 1 ") {
		t.Errorf("ecdsa: %v", r)
	}
	// Cross-check with ssh-keygen -r when available.
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not available")
	}
	out, err := exec.Command(keygen, "-r", "host", "-f", path+".pub").Output()
	if err != nil {
		t.Skipf("ssh-keygen -r: %v", err)
	}
	for _, want := range recs {
		if !strings.Contains(strings.ToLower(string(out)), "in sshfp "+want) {
			t.Errorf("ssh-keygen -r output lacks %q:\n%s", want, out)
		}
	}
}
