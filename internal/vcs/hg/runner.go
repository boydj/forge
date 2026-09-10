// Package hg implements vcs.Backend and vcs.Repository with the Mercurial
// command-line tool. It is the M11 prototype that validates the VCS
// abstraction against a second system; see docs/mercurial.md for what maps
// cleanly and what does not.
//
// Every invocation uses a fixed, hardened environment: HGPLAIN disables
// aliases, defaults, colour and localisation; HGRCPATH=/dev/null hides every
// user and system configuration file; the remaining knobs are pinned with
// --config on each command line. Only the repository's own .hg/hgrc (written
// by the forge, never by clients) is consulted.
package hg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"as215520.net/forge/internal/vcs"
)

// Options configure the backend.
type Options struct {
	// Binary is the hg executable; "hg" resolves from PATH.
	Binary string
	// Timeout bounds plumbing commands (0: 30s).
	Timeout time.Duration
	// MaxConcurrent limits simultaneous hg subprocesses (0: 16).
	MaxConcurrent int
	// HomeDir is HOME for subprocesses. Mercurial's own configuration is
	// disabled with HGRCPATH regardless; HOME matters only because a
	// pip-installed hg imports its modules from ~/.local (0: the process
	// HOME, else the temp dir).
	HomeDir string
	// MaxOutputBytes caps captured stdout of plumbing commands (0: 64 MiB).
	MaxOutputBytes int64
}

// Backend is the Mercurial implementation of vcs.Backend.
type Backend struct {
	opts Options
	sem  chan struct{}
}

// New returns a Backend. It verifies that the hg binary is runnable.
func New(opts Options) (*Backend, error) {
	if opts.Binary == "" {
		opts.Binary = "hg"
	}
	p, err := exec.LookPath(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("hg: %w", err)
	}
	opts.Binary = p
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 16
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = 64 << 20
	}
	if opts.HomeDir == "" {
		opts.HomeDir = os.Getenv("HOME")
		if opts.HomeDir == "" {
			opts.HomeDir = os.TempDir()
		}
	}
	b := &Backend{opts: opts, sem: make(chan struct{}, opts.MaxConcurrent)}
	out, err := b.runIn(context.Background(), "", "version", "-q")
	if err != nil {
		return nil, fmt.Errorf("hg: cannot run %s: %w", opts.Binary, err)
	}
	if !bytes.Contains(out, []byte("Mercurial")) {
		return nil, fmt.Errorf("hg: unexpected version output %q", out)
	}
	return b, nil
}

// Name implements vcs.Backend.
func (b *Backend) Name() string { return "hg" }

// Version returns the Mercurial version string ("7.2.4").
func (b *Backend) Version(ctx context.Context) string {
	out, err := b.runIn(ctx, "", "version", "-q")
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(string(out), "\n")
	if i := strings.Index(line, "(version "); i >= 0 {
		return strings.TrimSuffix(line[i+len("(version "):], ")")
	}
	return strings.TrimSpace(line)
}

// Env returns the hardened environment for an hg subprocess.
func (b *Backend) Env(extra ...string) []string {
	env := []string{
		"PATH=" + filepath.Dir(b.opts.Binary) + ":/usr/local/bin:/usr/bin:/bin",
		"HOME=" + b.opts.HomeDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HGPLAIN=1",
		"HGRCPATH=/dev/null",
		"HGENCODING=UTF-8",
		"HGUSER=forge",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	return append(env, extra...)
}

// configArgs are prepended to every command line. HGRCPATH already hides
// user and system hgrc files (and with them every extension); these pin the
// remaining behaviour that a repository-level hgrc could otherwise change.
var configArgs = []string{
	"--config", "ui.interactive=false",
	"--config", "ui.paginate=false",
	"--config", "ui.report_untrusted=false",
	"--config", "ui.color=never",
	"--config", "ui.verbose=false",
	"--config", "ui.debug=false",
	"--config", "ui.traceback=false",
	"--config", "ui.tweakdefaults=false",
	"--config", "phases.publish=true",
	"--config", "server.bundle1=false",
	"--config", "experimental.evolution=",
	"--config", "extensions.evolve=!",
	"--config", "extensions.topic=!",
	"--config", "extensions.largefiles=!",
	"--config", "extensions.lfs=!",
}

// Command builds an *exec.Cmd for a transport or long-running operation
// (serve --stdio, pull). The caller owns its lifetime.
func (b *Backend) Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(configArgs)+len(args)+2)
	if dir != "" {
		full = append(full, "-R", dir)
	}
	full = append(full, configArgs...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, b.opts.Binary, full...)
	cmd.Env = b.Env()
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Acquire takes a concurrency slot; the returned func releases it.
func (b *Backend) Acquire(ctx context.Context) (func(), error) {
	select {
	case b.sem <- struct{}{}:
		return func() { <-b.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runIn executes a plumbing command against the repository at dir ("" for
// none) with the default timeout and returns stdout. Stderr is folded into
// the error.
func (b *Backend) runIn(ctx context.Context, dir string, args ...string) ([]byte, error) {
	release, err := b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()
	cmd := b.Command(ctx, dir, args...)
	var stdout limitedBuffer
	stdout.max = b.opts.MaxOutputBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	err = cmd.Run()
	if stdout.overflow {
		return nil, vcs.ErrTooLarge
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, vcs.ErrTimeout
		}
		return stdout.buf.Bytes(), &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return stdout.buf.Bytes(), nil
}

// stream starts a plumbing command and hands its stdout to fn. The process
// is killed when fn returns; its exit status is reported only when fn did
// not fail and did not stop early (stopped is returned true by fn to say it
// consumed as much as it wanted).
func (b *Backend) stream(ctx context.Context, dir string, args []string, fn func(io.Reader) (stopped bool, err error)) error {
	release, err := b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()
	cmd := b.Command(cctx, dir, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	if err := cmd.Start(); err != nil {
		return err
	}
	stopped, ferr := fn(stdout)
	if stopped || ferr != nil {
		cancel()
	}
	werr := cmd.Wait()
	if ferr != nil {
		return ferr
	}
	if stopped {
		return nil
	}
	if werr != nil {
		if cctx.Err() != nil {
			return vcs.ErrTimeout
		}
		return &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: werr}
	}
	return nil
}

// Error carries hg's stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("hg %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode returns the process exit code or -1.
func (e *Error) ExitCode() int {
	var ee *exec.ExitError
	if errors.As(e.Err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// notFoundOr maps hg's "abort: unknown revision", "empty revision set",
// missing bookmark/tag/file messages to vcs.ErrNotFound.
func notFoundOr(err error) error {
	var he *Error
	if !errors.As(err, &he) {
		return err
	}
	for _, s := range []string{
		"unknown revision", "empty revision set", "empty revision range",
		"does not exist", "no such file", "not found", "filtered revision",
		"not under root",
	} {
		if strings.Contains(he.Stderr, s) {
			return vcs.ErrNotFound
		}
	}
	return err
}

type limitedBuffer struct {
	buf      bytes.Buffer
	max      int64
	overflow bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if int64(l.buf.Len())+int64(len(p)) > l.max {
		l.overflow = true
		return 0, io.ErrShortWrite
	}
	return l.buf.Write(p)
}

type limitedWriter struct {
	w   io.Writer
	max int64
	n   int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n >= l.max {
		return len(p), nil
	}
	if l.n+int64(len(p)) > l.max {
		p = p[:l.max-l.n]
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return len(p), err
}

// defaultBranchFile records the forge's notion of the default branch inside
// the repository. Mercurial has no HEAD; the closest native concept is the
// "@" bookmark, which clients may move. The forge keeps its own record.
const defaultBranchFile = "forge-default-branch"

// Init implements vcs.Backend: `hg init` (no working copy is ever updated).
func (b *Backend) Init(ctx context.Context, path, defaultBranch string) error {
	if err := checkName(defaultBranch); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("hg: %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := b.runIn(ctx, "", "init", "--", path); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(path, ".hg", defaultBranchFile), []byte(defaultBranch+"\n"), 0o640); err != nil {
		return err
	}
	// The repository hgrc is ours: it is never sent over the wire and only
	// the forge writes it. Pin the server-side behaviour here too so that
	// `hg serve --stdio` (which does not go through configArgs) sees it.
	hgrc := "# managed by forge; do not edit\n[phases]\npublish = true\n[server]\nbundle1 = false\n[ui]\ninteractive = false\n"
	return os.WriteFile(filepath.Join(path, ".hg", "hgrc"), []byte(hgrc), 0o640)
}

// SetDefaultBranch implements vcs.Backend. The name must resolve to a
// branch or bookmark in the repository (unless it is "default", which is
// always acceptable for an empty repository).
func (b *Backend) SetDefaultBranch(ctx context.Context, path, branch string) error {
	if err := checkName(branch); err != nil {
		return err
	}
	repo, err := b.Open(path)
	if err != nil {
		return err
	}
	if branch != "default" {
		if _, err := repo.Resolve(ctx, branch); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(path, ".hg", defaultBranchFile), []byte(branch+"\n"), 0o640)
}

// Open implements vcs.Backend.
func (b *Backend) Open(path string) (vcs.Repository, error) {
	st, err := os.Stat(filepath.Join(path, ".hg", "requires"))
	if err != nil || st.IsDir() {
		return nil, vcs.ErrNotFound
	}
	return &Repo{b: b, path: path}, nil
}

// Fetch implements vcs.Backend: `hg pull -f` from remoteURL into path.
// Changesets, bookmarks and (through .hgtags) tags are replicated; Mercurial
// pull never deletes, so a bookmark removed at the source persists here.
func (b *Backend) Fetch(ctx context.Context, path, remoteURL string) error {
	if remoteURL == "" || strings.HasPrefix(remoteURL, "-") {
		return vcs.ErrBadPath
	}
	release, err := b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cmd := b.Command(ctx, path, "pull", "-q", "-f", "--", remoteURL)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	if err := cmd.Run(); err != nil {
		return &Error{Args: []string{"pull"}, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return nil
}

// checkName validates a branch, bookmark or tag name before it is embedded
// in a revset string literal or passed as an argument. Mercurial itself
// forbids ":" and "\0" and all-whitespace names; the extra rules keep the
// name out of the revset parser and off the option parser.
func checkName(name string) error {
	if name == "" || len(name) > 255 || strings.HasPrefix(name, "-") || strings.TrimSpace(name) == "" {
		return vcs.ErrBadRef
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == ':' {
			return vcs.ErrBadRef
		}
	}
	return nil
}

// isHex reports whether s is a 4..40 character lower/upper hex string.
func isHex(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// checkID validates a changeset id: a full 40-hex node (prefixes are
// accepted for Resolve only).
func checkID(id vcs.RevisionID) error {
	if len(id) != 40 || !isHex(string(id)) {
		return vcs.ErrBadRef
	}
	return nil
}

func checkIDs(ids ...vcs.RevisionID) error {
	for _, id := range ids {
		if err := checkID(id); err != nil {
			return err
		}
	}
	return nil
}

// checkPath validates a tree path: relative, canonical, no traversal, and
// safe to embed in a revset string or a "path:" pattern.
func checkPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.ContainsAny(p, "\x00\"\\") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return "", vcs.ErrBadPath
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", vcs.ErrBadPath
		}
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "." {
		return "", nil
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
		return "", vcs.ErrBadPath
	}
	if clean != strings.TrimSuffix(p, "/") {
		return "", vcs.ErrBadPath
	}
	return clean, nil
}

// revsetID renders id("<node>") for a validated id.
func revsetID(id vcs.RevisionID) string { return "id(\"" + string(id) + "\")" }

// revsetPath renders file("path:<p>") for a validated path.
func revsetPath(p string) string { return "file(\"path:" + p + "\")" }
