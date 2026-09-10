package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := Default(dir)
	c.Hostname = "git.example.org"
	c.SSH.Port = 2222
	p := filepath.Join(dir, "forge.toml")
	if err := c.Write(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "git.example.org" || got.SSH.Port != 2222 || got.SSH.HandshakeTimeout != c.SSH.HandshakeTimeout {
		t.Errorf("mismatch: %+v", got)
	}
	if got.CloneURL("alice", "proj") != "ssh://git@git.example.org:2222/alice/proj.git" {
		t.Errorf("clone url %s", got.CloneURL("alice", "proj"))
	}
	got.SSH.Port = 22
	if got.CloneURL("alice", "proj") != "git@git.example.org:alice/proj.git" {
		t.Errorf("clone url %s", got.CloneURL("alice", "proj"))
	}
	if got.GeminiURL("~alice/proj/") != "gemini://git.example.org/~alice/proj/" {
		t.Errorf("gemini url %s", got.GeminiURL("~alice/proj/"))
	}
}

func TestUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(p, []byte("hostname = \"x\"\nbogus = 1\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Error("expected unknown key error")
	}
}
