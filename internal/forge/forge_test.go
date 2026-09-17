package forge

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
	"as215520.net/forge/pkg/config"
)

func newForge(t *testing.T) *Forge {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default(dir)
	cfg.Limits.MinFreeBytes = 0
	st, err := store.Open(context.Background(), filepath.Join(dir, "f.db"), "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g, err := gitvcs.New(gitvcs.Options{HomeDir: dir, HooksDir: filepath.Join(dir, "hooks")})
	if err != nil {
		t.Skip(err)
	}
	f := New(cfg, st, g, slog.Default())
	if err := f.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	return f
}

func selfSigned(t *testing.T, cn string, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

func TestIdentityAndRegistration(t *testing.T) {
	f := newForge(t)
	ctx := context.Background()
	id, err := f.Authenticate(ctx, nil)
	if err != nil || !id.Anonymous() {
		t.Fatalf("anon: %v", err)
	}
	cert := selfSigned(t, "alice", time.Now().Add(24*time.Hour))
	id, err = f.Authenticate(ctx, cert)
	if err != ErrCertUnknown || id.SPKI == "" {
		t.Fatalf("unknown cert: %v", err)
	}
	if _, err := f.Register(ctx, id, "Alice!"); err != ErrInvalidName {
		t.Errorf("bad name: %v", err)
	}
	if _, err := f.Register(ctx, id, "account"); err != ErrReservedName {
		t.Errorf("reserved: %v", err)
	}
	u, err := f.Register(ctx, id, "alice")
	if err != nil || !u.Admin {
		t.Fatalf("register: %v admin=%v", err, u != nil && u.Admin)
	}
	id2, err := f.Authenticate(ctx, cert)
	if err != nil || id2.User == nil || id2.User.ID != u.ID {
		t.Fatalf("auth after register: %v", err)
	}
	if _, err := f.Register(ctx, id2, "bob"); err != ErrExists {
		t.Errorf("double register: %v", err)
	}
	expired := selfSigned(t, "old", time.Now().Add(-time.Hour))
	if _, err := f.Authenticate(ctx, expired); err != ErrCertExpired {
		t.Errorf("expired: %v", err)
	}
	_ = f.Store.RevokeCertificate(ctx, id2.Cert.ID)
	if _, err := f.Authenticate(ctx, cert); err != ErrCertRevoked {
		t.Errorf("revoked: %v", err)
	}
}

func TestReposAndPermissions(t *testing.T) {
	f := newForge(t)
	ctx := context.Background()
	alice, _ := f.Store.CreateUser(ctx, "alice", false)
	bob, _ := f.Store.CreateUser(ctx, "bob", false)
	if _, err := f.CreateRepo(ctx, alice, CreateRepoOptions{Name: "Bad Name"}); err != ErrInvalidName {
		t.Errorf("bad name: %v", err)
	}
	if _, err := f.CreateRepo(ctx, alice, CreateRepoOptions{Name: "feed"}); err != ErrReservedName {
		t.Errorf("reserved: %v", err)
	}
	r, err := f.CreateRepo(ctx, alice, CreateRepoOptions{Name: "proj", Description: "a project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateRepo(ctx, alice, CreateRepoOptions{Name: "proj"}); err != ErrExists {
		t.Errorf("dup: %v", err)
	}
	repo, err := f.Open(r)
	if err != nil {
		t.Fatal(err)
	}
	if empty, _ := repo.Empty(ctx); !empty {
		t.Error("new repo should be empty")
	}
	a, err := f.LookupRepo(ctx, nil, "alice", "proj")
	if err != nil || !a.CanRead() || a.CanWrite() {
		t.Errorf("anon access: %v %+v", err, a)
	}
	a, _ = f.LookupRepo(ctx, alice, "alice", "proj")
	if !a.CanAdmin() {
		t.Error("owner should admin")
	}
	priv := true
	if err := f.UpdateRepo(ctx, bob, r, UpdateRepoOptions{Private: &priv}); err != ErrForbidden {
		t.Errorf("bob update: %v", err)
	}
	if err := f.UpdateRepo(ctx, alice, r, UpdateRepoOptions{Private: &priv}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LookupRepo(ctx, nil, "alice", "proj"); err != ErrNotFound {
		t.Errorf("private anon: %v", err)
	}
	if _, err := f.LookupRepo(ctx, bob, "alice", "proj"); err != ErrNotFound {
		t.Errorf("private bob: %v", err)
	}
	_ = f.Store.SetCollaborator(ctx, r.ID, bob.ID, store.RoleWrite)
	a, err = f.LookupRepo(ctx, bob, "alice", "proj")
	if err != nil || !a.CanWrite() || a.CanAdmin() {
		t.Errorf("collab: %v %+v", err, a)
	}
	if err := f.DeleteRepo(ctx, alice, r, "wrong"); err != ErrNotAcceptable {
		t.Errorf("delete confirm: %v", err)
	}
	if err := f.DeleteRepo(ctx, alice, r, "alice/proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LookupRepo(ctx, alice, "alice", "proj"); err != ErrNotFound {
		t.Error("deleted repo visible")
	}
	n, err := f.PurgeDeleted(ctx)
	if err != nil || n != 0 {
		t.Errorf("purge before retention: %d %v", n, err)
	}
}

func TestSSHKeys(t *testing.T) {
	f := newForge(t)
	ctx := context.Background()
	alice, _ := f.Store.CreateUser(ctx, "alice", false)
	bob, _ := f.Store.CreateUser(ctx, "bob", false)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	line := string(ssh.MarshalAuthorizedKey(sshPub))
	text := "# comment\n\n" + line[:len(line)-1] + " laptop\n"
	added, err := f.AddSSHKeys(ctx, alice, text)
	if err != nil || len(added) != 1 || added[0].Label != "laptop" || added[0].KeyType != "ssh-ed25519" {
		t.Fatalf("add: %v %+v", err, added)
	}
	if added, err := f.AddSSHKeys(ctx, alice, text); err != nil || len(added) != 0 {
		t.Errorf("re-add: %v %d", err, len(added))
	}
	if _, err := f.AddSSHKeys(ctx, bob, text); err == nil {
		t.Error("key owned by alice accepted for bob")
	}
	if _, err := f.AddSSHKeys(ctx, alice, "garbage"); err == nil {
		t.Error("garbage accepted")
	}
	u, _, err := f.UserForSSHKey(ctx, sshPub)
	if err != nil || u.ID != alice.ID {
		t.Errorf("lookup: %v", err)
	}
	if err := f.RemoveSSHKey(ctx, bob, added[0].Fingerprint); err != ErrNotFound {
		t.Errorf("bob remove: %v", err)
	}
	if err := f.RemoveSSHKey(ctx, alice, added[0].Fingerprint); err != nil {
		t.Errorf("remove: %v", err)
	}
	if _, _, err := f.UserForSSHKey(ctx, sshPub); err != ErrNotFound {
		t.Errorf("after remove: %v", err)
	}
}
