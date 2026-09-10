package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrateAndUsers(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	v, _ := s.SchemaVersion(ctx)
	if v < 1 {
		t.Fatalf("schema version %d", v)
	}
	// Reopen applies nothing new.
	s2, err := Open(ctx, filepath.Join(filepath.Dir(t.TempDir()), "x.db"), "test")
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	u, err := s.CreateUser(ctx, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice", false); err != ErrConflict {
		t.Errorf("dup user: %v", err)
	}
	c, err := s.AddCertificate(ctx, &Certificate{UserID: u.ID, SPKISHA256: "aa", CertSHA256: "bb", Label: "laptop", NotAfter: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CertificateBySPKI(ctx, "aa")
	if err != nil || got.ID != c.ID || got.NotAfter.IsZero() {
		t.Errorf("cert lookup: %v %+v", err, got)
	}
	_ = s.RevokeCertificate(ctx, c.ID)
	got, _ = s.CertificateBySPKI(ctx, "aa")
	if got.RevokedAt.IsZero() {
		t.Error("revoke not recorded")
	}
	k, err := s.AddSSHKey(ctx, &SSHKey{UserID: u.ID, Fingerprint: "SHA256:x", KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSSHKey(ctx, &SSHKey{UserID: u.ID, Fingerprint: "SHA256:x", KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA"}); err != ErrConflict {
		t.Errorf("dup key: %v", err)
	}
	if err := s.DeleteSSHKey(ctx, u.ID, k.ID); err != nil {
		t.Error(err)
	}
	if err := s.DeleteSSHKey(ctx, u.ID, k.ID); err != ErrNotFound {
		t.Errorf("delete missing: %v", err)
	}
	if err := s.CreateToken(ctx, u.ID, "enroll", "h1", "", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if tok, err := s.ConsumeToken(ctx, "enroll", "h1"); err != nil || tok.UserID != u.ID {
		t.Errorf("consume: %v %+v", err, tok)
	}
	if _, err := s.ConsumeToken(ctx, "enroll", "h1"); err != ErrNotFound {
		t.Errorf("double consume: %v", err)
	}
}

func TestReposAndEvents(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	alice, _ := s.CreateUser(ctx, "alice", false)
	bob, _ := s.CreateUser(ctx, "bob", false)
	admin, _ := s.CreateUser(ctx, "root", true)
	pub, err := s.CreateRepo(ctx, &Repo{OwnerID: alice.ID, Name: "pub", DefaultBranch: "main", VCS: "git", LeaderNode: "test"})
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := s.CreateRepo(ctx, &Repo{OwnerID: alice.ID, Name: "priv", Private: true, DefaultBranch: "main", VCS: "git", LeaderNode: "test"})
	if _, err := s.CreateRepo(ctx, &Repo{OwnerID: alice.ID, Name: "pub", DefaultBranch: "main", VCS: "git", LeaderNode: "test"}); err != ErrConflict {
		t.Errorf("dup repo: %v", err)
	}
	if r, err := s.RepoByPath(ctx, "alice", "pub"); err != nil || r.Owner != "alice" || r.ID != pub.ID {
		t.Errorf("by path: %v %+v", err, r)
	}
	count := func(viewer int64) int {
		rs, err := s.ListRepos(ctx, RepoListOptions{Viewer: viewer})
		if err != nil {
			t.Fatal(err)
		}
		return len(rs)
	}
	if count(0) != 1 || count(alice.ID) != 2 || count(bob.ID) != 1 || count(admin.ID) != 2 {
		t.Errorf("visibility: anon=%d alice=%d bob=%d admin=%d", count(0), count(alice.ID), count(bob.ID), count(admin.ID))
	}
	_ = s.SetCollaborator(ctx, priv.ID, bob.ID, RoleRead)
	if count(bob.ID) != 2 {
		t.Error("collaborator should see private repo")
	}
	if role, _ := s.CollaboratorRole(ctx, priv.ID, bob.ID); role != RoleRead {
		t.Errorf("role %q", role)
	}
	_ = s.SetCollaborator(ctx, priv.ID, bob.ID, RoleNone)
	if count(bob.ID) != 1 {
		t.Error("removed collaborator still sees repo")
	}
	_, _ = s.AddEvent(ctx, &Event{Kind: EventPush, RepoID: pub.ID, UserID: alice.ID, Subject: "push to main", Path: "/~alice/pub/log/main"})
	_, _ = s.AddEvent(ctx, &Event{Kind: EventPush, RepoID: priv.ID, UserID: alice.ID, Subject: "secret push", Path: "/~alice/priv/log/main"})
	evs, _ := s.Events(ctx, EventQuery{Viewer: 0})
	if len(evs) != 1 || evs[0].Owner != "alice" || evs[0].RepoName != "pub" || evs[0].UserName != "alice" {
		t.Errorf("anon events: %+v", evs)
	}
	evs, _ = s.Events(ctx, EventQuery{Viewer: alice.ID})
	if len(evs) != 2 || evs[0].Subject != "secret push" {
		t.Errorf("owner events: %d", len(evs))
	}
	evs, _ = s.Events(ctx, EventQuery{Viewer: alice.ID, AfterID: 1})
	if len(evs) != 1 || evs[0].ID != 2 {
		t.Errorf("after id: %+v", evs)
	}
	_ = s.SoftDeleteRepo(ctx, pub.ID)
	if _, err := s.RepoByPath(ctx, "alice", "pub"); err != ErrNotFound {
		t.Error("soft-deleted repo still visible")
	}
	if evs, _ := s.Events(ctx, EventQuery{Viewer: 0}); len(evs) != 0 {
		t.Error("events of deleted repo visible")
	}
	dels, _ := s.ListDeletedRepos(ctx, time.Now().Add(time.Hour))
	if len(dels) != 1 {
		t.Errorf("deleted list: %d", len(dels))
	}
	if err := s.Backup(ctx, filepath.Join(t.TempDir(), "b.db")); err != nil {
		t.Errorf("backup: %v", err)
	}
}
