package web

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/tlsid"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

type harness struct {
	t    *testing.T
	f    *forge.Forge
	addr string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default(dir)
	cfg.Hostname = "localhost"
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
	f := forge.New(cfg, st, g, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := f.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cert, _, err := tlsid.LoadOrCreate(cfg.Gemini.CertFile, cfg.Gemini.KeyFile, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &gemini.Server{Handler: New(f, f.Log), TLSConfig: gemini.TLSServerConfig(cert), Logger: f.Log}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &harness{t: t, f: f, addr: l.Addr().String()}
}

func clientCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// get performs one Gemini or Titan request. body is sent after the request
// line for Titan.
func (h *harness) get(rawurl string, cert *tls.Certificate, body string) (int, string, string) {
	h.t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := tls.Dial("tcp", h.addr, cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s\r\n", rawurl)
	if body != "" {
		_, _ = io.WriteString(conn, body)
	}
	br := bufio.NewReader(conn)
	header, err := br.ReadString('\n')
	if err != nil {
		h.t.Fatalf("header: %v", err)
	}
	header = strings.TrimRight(header, "\r\n")
	var status int
	fmt.Sscanf(header, "%d", &status)
	meta := ""
	if len(header) > 3 {
		meta = header[3:]
	}
	rest, _ := io.ReadAll(br)
	return status, meta, string(rest)
}

func (h *harness) url(path string) string {
	_, port, _ := net.SplitHostPort(h.addr)
	return "gemini://localhost:" + port + path
}

func (h *harness) titan(path string, mime string, body string) string {
	_, port, _ := net.SplitHostPort(h.addr)
	return fmt.Sprintf("titan://localhost:%s%s;size=%d;mime=%s", port, path, len(body), mime)
}

func (h *harness) seedRepo(user, name string) {
	h.t.Helper()
	ctx := context.Background()
	u, err := h.f.Store.UserByName(ctx, user)
	if err != nil {
		u, err = h.f.Store.CreateUser(ctx, user, false)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	r, err := h.f.CreateRepo(ctx, u, forge.CreateRepoOptions{Name: name, Description: "seeded"})
	if err != nil {
		h.t.Fatal(err)
	}
	work := filepath.Join(h.t.TempDir(), "w")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			h.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_ = os.MkdirAll(work, 0o755)
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# Hello\n\nSee [docs](docs/a.md) and =>injected\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(work, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(work, "docs", "a.md"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "evil.txt"), []byte("=> gemini://evil/ click\n```\n# heading\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "first")
	run("tag", "v0.1")
	run("push", "-q", h.f.RepoPath(r.Owner, r.Name), "main", "v0.1")
}

func TestPages(t *testing.T) {
	h := newHarness(t)
	h.seedRepo("alice", "proj")
	cases := []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 20, []string{"# forge", "=> /~alice/proj/ alice/proj - seeded"}},
		{"/~alice", 31, nil},
		{"/~alice/", 20, []string{"# alice", "=> /~alice/proj/ proj - seeded"}},
		{"/~alice/proj", 31, nil},
		{"/~alice/proj/", 20, []string{"git clone git@localhost:alice/proj.git", "## README.md", "# Hello", "=> /~alice/proj/tree/main/docs/a.md docs", " =>injected"}},
		{"/~alice/proj/tree/main/", 20, []string{"=> /~alice/proj/tree/main/docs/ docs/", "README.md (", "evil.txt ("}},
		{"/~alice/proj/tree/main/docs", 31, nil},
		{"/~alice/proj/tree/main/evil.txt", 20, []string{"```evil.txt\n=> gemini://evil/ click\n ```\n# heading\n```"}},
		{"/~alice/proj/raw/main/README.md", 20, []string{"# Hello"}},
		{"/~alice/proj/log/main", 20, []string{"first (T)"}},
		{"/~alice/proj/refs", 20, []string{"main (default)", "v0.1"}},
		{"/~alice/proj/feed", 20, []string{"# alice/proj activity", " - alice created alice/proj"}},
		{"/~alice/proj/atom.xml", 20, []string{"<feed xmlns=\"http://www.w3.org/2005/Atom\">", "<title>alice/proj activity</title>"}},
		{"/~alice/nope/", 51, nil},
		{"/~nobody/", 51, nil},
		{"/~alice/proj/tree/main/../../etc", 59, nil},
		{"/status", 20, []string{"ok"}},
		{"/account", 60, nil},
		{"/new", 60, nil},
		{"/feed", 20, []string{"2026"}},
	}
	for _, c := range cases {
		status, meta, body := h.get(h.url(c.path), nil, "")
		if status != c.status {
			t.Errorf("%s: status %d %q, want %d\n%s", c.path, status, meta, c.status, body)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q in:\n%s", c.path, w, body)
			}
		}
	}
	// Commit page via refs
	_, _, body := h.get(h.url("/~alice/proj/log/main"), nil, "")
	var id string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "=> /~alice/proj/commit/") {
			id = strings.Fields(l)[1]
			break
		}
	}
	if id == "" {
		t.Fatal("no commit link")
	}
	status, _, body := h.get(h.url(id), nil, "")
	if status != 20 || !strings.Contains(body, "+# Hello") || !strings.Contains(body, "* README.md +3 -0") {
		t.Errorf("commit page: %d\n%s", status, body)
	}
}

func TestRegistrationAndKeys(t *testing.T) {
	h := newHarness(t)
	cert := clientCert(t, "alice")
	status, _, body := h.get(h.url("/account"), &cert, "")
	if status != 20 || !strings.Contains(body, "not registered") {
		t.Fatalf("unknown cert: %d %s", status, body)
	}
	status, meta, _ := h.get(h.url("/account?%3F"), &cert, "")
	if status != 10 {
		t.Fatalf("expected input prompt, got %d %s", status, meta)
	}
	status, meta, _ = h.get(h.url("/account?Bad%20Name"), &cert, "")
	if status != 10 || !strings.Contains(meta, "not available") {
		t.Fatalf("bad name: %d %s", status, meta)
	}
	status, meta, _ = h.get(h.url("/account?alice"), &cert, "")
	if status != 30 || meta != "/account" {
		t.Fatalf("register: %d %s", status, meta)
	}
	status, _, body = h.get(h.url("/account"), &cert, "")
	if status != 20 || !strings.Contains(body, "Account: alice") || !strings.Contains(body, "administrator") {
		t.Fatalf("account page: %d %s", status, body)
	}
	// Second cert cannot take the same name.
	cert2 := clientCert(t, "bob")
	status, meta, _ = h.get(h.url("/account?alice"), &cert2, "")
	if status != 10 || !strings.Contains(meta, "taken") {
		t.Fatalf("dup name: %d %s", status, meta)
	}
	// Create a repository through INPUT.
	status, meta, _ = h.get(h.url("/new"), &cert, "")
	if status != 10 {
		t.Fatalf("new prompt: %d", status)
	}
	status, meta, _ = h.get(h.url("/new?myrepo"), &cert, "")
	if status != 30 || meta != "/~alice/myrepo/" {
		t.Fatalf("create: %d %s", status, meta)
	}
	status, _, body = h.get(h.url("/~alice/myrepo/"), nil, "")
	if status != 20 || !strings.Contains(body, "This repository is empty") {
		t.Fatalf("empty repo page: %d %s", status, body)
	}
	// Titan upload of authorized_keys.
	pub, err := os.ReadFile(genKey(t))
	if err != nil {
		t.Fatal(err)
	}
	keys := string(pub)
	status, meta, _ = h.get(h.titan("/account/keys", "text/plain", keys), &cert, keys)
	if status != 30 || meta != "/account/keys" {
		t.Fatalf("titan keys: %d %s", status, meta)
	}
	status, _, body = h.get(h.url("/account/keys"), &cert, "")
	if status != 20 || !strings.Contains(body, "ssh-ed25519") || !strings.Contains(body, "SHA256:") {
		t.Fatalf("keys page: %d %s", status, body)
	}
	// Titan without a certificate is refused before reading the body.
	status, _, _ = h.get(h.titan("/account/keys", "text/plain", keys), nil, keys)
	if status != 60 {
		t.Errorf("titan anon: %d", status)
	}
	// Titan with an unregistered certificate is refused.
	status, _, _ = h.get(h.titan("/account/keys", "text/plain", keys), &cert2, keys)
	if status != 61 {
		t.Errorf("titan unregistered: %d", status)
	}
	// Wrong MIME.
	status, _, _ = h.get(h.titan("/account/keys", "image/png", keys), &cert, keys)
	if status != 50 {
		t.Errorf("titan mime: %d", status)
	}
	// Enrolment code flow for a second device.
	status, _, body = h.get(h.url("/account/certs/enrol-code"), &cert, "")
	if status != 20 {
		t.Fatalf("enrol code: %d", status)
	}
	code := ""
	for _, l := range strings.Split(body, "\n") {
		if len(l) == 16 && !strings.ContainsAny(l, " `") {
			code = l
		}
	}
	if code == "" {
		t.Fatalf("no code in %s", body)
	}
	status, meta, _ = h.get(h.url("/account/enrol"), &cert2, "")
	if status != 11 {
		t.Fatalf("enrol prompt: %d %s", status, meta)
	}
	status, meta, _ = h.get(h.url("/account/enrol?"+code), &cert2, "")
	if status != 30 {
		t.Fatalf("enrol: %d %s", status, meta)
	}
	status, _, body = h.get(h.url("/account"), &cert2, "")
	if status != 20 || !strings.Contains(body, "Account: alice") {
		t.Fatalf("second device: %d %s", status, body)
	}
	cert3 := clientCert(t, "c")
	status, meta, _ = h.get(h.url("/account/enrol?"+code), &cert3, "")
	if status != 11 {
		t.Errorf("code reuse: %d %s", status, meta)
	}
}

func genKey(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "laptop", "-f", filepath.Join(dir, "k")).CombinedOutput()
	if err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	return filepath.Join(dir, "k.pub")
}

func TestMarkdown(t *testing.T) {
	in := "# Title\n\nPara one\ncontinues [here](x.md).\n\n- a\n- b\n\n```go\nfmt.Println()\n```\n\n> quote\n"
	got := MarkdownToGemtext(in, "/base/")
	want := "# Title\n\nPara one continues here.\n=> /base/x.md here\n\n* a\n* b\n\n```go\nfmt.Println()\n```\n\n> quote\n\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
