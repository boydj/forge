// Package git implements vcs.Backend and vcs.Repository with the git
// command-line tool. Every invocation uses a fixed, hardened environment; no
// configuration from the user's environment or from the repository's working
// tree is honoured (bare repositories have none).
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"as215520.net/forge/internal/vcs"
)

// Options configure the backend.
type Options struct {
	// Binary is the git executable; "git" resolves from PATH.
	Binary string
	// Timeout bounds read-only plumbing commands.
	Timeout time.Duration
	// MaxConcurrent limits simultaneous git subprocesses (0: 16).
	MaxConcurrent int
	// HooksDir is the directory containing pre-receive/update/post-receive.
	HooksDir string
	// HomeDir is used as HOME for subprocesses (must not contain a .gitconfig).
	HomeDir string
	// MaxInputSize is receive.maxInputSize (0: unlimited).
	MaxInputSize int64
	// MaxOutputBytes caps captured stdout of plumbing commands (0: 64 MiB).
	MaxOutputBytes int64
}

// Backend is the git implementation of vcs.Backend.
type Backend struct {
	opts Options
	sem  chan struct{}
}

// New returns a Backend. It verifies that the git binary is runnable.
func New(opts Options) (*Backend, error) {
	if opts.Binary == "" {
		opts.Binary = "git"
	}
	p, err := exec.LookPath(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
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
	b := &Backend{opts: opts, sem: make(chan struct{}, opts.MaxConcurrent)}
	out, err := b.runIn(context.Background(), "", "--version")
	if err != nil {
		return nil, fmt.Errorf("git: cannot run %s: %w", opts.Binary, err)
	}
	if !bytes.HasPrefix(out, []byte("git version")) {
		return nil, fmt.Errorf("git: unexpected --version output %q", out)
	}
	return b, nil
}

// Name implements vcs.Backend.
func (b *Backend) Name() string { return "git" }

// Version returns the git version string.
func (b *Backend) Version(ctx context.Context) string {
	out, err := b.runIn(ctx, "", "--version")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(strings.TrimPrefix(string(out), "git version "))
}

// Env returns the hardened environment for a git subprocess. Configuration
// is injected with GIT_CONFIG_COUNT so it applies regardless of any
// configuration file (git >= 2.31).
func (b *Backend) Env(extra ...string) []string {
	home := b.opts.HomeDir
	if home == "" {
		home = os.TempDir()
	}
	cfg := [][2]string{
		{"core.hooksPath", b.opts.HooksDir},
		{"core.protectNTFS", "true"},
		{"core.protectHFS", "true"},
		{"core.fsmonitor", "false"},
		{"transfer.fsckObjects", "true"},
		{"receive.fsckObjects", "true"},
		{"fetch.fsckObjects", "true"},
		{"receive.autogc", "false"},
		{"gc.auto", "0"},
		{"receive.advertisePushOptions", "false"},
		{"receive.denyCurrentBranch", "ignore"},
		{"uploadpack.allowAnySHA1InWant", "false"},
		{"uploadpack.allowReachableSHA1InWant", "false"},
		{"uploadpack.allowFilter", "true"},
		{"uploadpack.allowRefInWant", "true"},
		{"protocol.version", "2"},
		{"protocol.allow", "never"},
		{"protocol.ssh.allow", "never"},
		{"protocol.file.allow", "always"},
		{"protocol.ext.allow", "never"},
		{"safe.directory", "*"},
		{"advice.detachedHead", "false"},
		{"color.ui", "never"},
		{"i18n.logOutputEncoding", "utf-8"},
	}
	if b.opts.HooksDir == "" {
		cfg[0] = [2]string{"core.hooksPath", "/nonexistent"}
	}
	if b.opts.MaxInputSize > 0 {
		cfg = append(cfg, [2]string{"receive.maxInputSize", strconv.FormatInt(b.opts.MaxInputSize, 10)})
	}
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + home,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_ASKPASS=/bin/false",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(cfg)),
	}
	for i, kv := range cfg {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return append(env, extra...)
}

// Command builds an *exec.Cmd for a transport or long-running operation
// (upload-pack, receive-pack, fetch). The caller owns its lifetime.
func (b *Backend) Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.opts.Binary, args...)
	cmd.Env = b.Env()
	if dir != "" {
		cmd.Dir = dir
	}
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

// runIn executes a plumbing command in dir with the default timeout and
// returns stdout. Stderr is folded into the error.
func (b *Backend) runIn(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return b.runInput(ctx, dir, nil, args...)
}

func (b *Backend) runInput(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	release, err := b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()
	cmd := b.Command(ctx, dir, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
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

// Error carries git's stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
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

// Init implements vcs.Backend.
func (b *Backend) Init(ctx context.Context, path, defaultBranch string) error {
	if err := checkBranchName(defaultBranch); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("git: %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := b.runIn(ctx, "", "init", "--bare", "--quiet", "--initial-branch="+defaultBranch, "--", path); err != nil {
		return err
	}
	// Repository-local settings that transport commands read from the
	// repository config. These are ours: users cannot modify this file.
	for _, kv := range [][2]string{
		{"core.logAllRefUpdates", "true"},
		{"core.sharedRepository", "0640"},
		{"gc.reflogExpire", "90 days"},
		{"gc.reflogExpireUnreachable", "30 days"},
	} {
		if _, err := b.runIn(ctx, path, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	// A description file is not used; remove the sample hooks directory so
	// nothing runnable lives inside the repository.
	_ = os.RemoveAll(filepath.Join(path, "hooks"))
	_ = os.Remove(filepath.Join(path, "description"))
	return nil
}

// SetDefaultBranch implements vcs.Backend.
func (b *Backend) SetDefaultBranch(ctx context.Context, path, branch string) error {
	if err := checkBranchName(branch); err != nil {
		return err
	}
	_, err := b.runIn(ctx, path, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return err
}

// Open implements vcs.Backend.
func (b *Backend) Open(path string) (vcs.Repository, error) {
	st, err := os.Stat(filepath.Join(path, "HEAD"))
	if err != nil || st.IsDir() {
		return nil, vcs.ErrNotFound
	}
	return &Repo{b: b, path: path}, nil
}

// Fetch implements vcs.Backend: mirror refs from remoteURL into path.
func (b *Backend) Fetch(ctx context.Context, path, remoteURL string) error {
	if strings.HasPrefix(remoteURL, "-") {
		return vcs.ErrBadPath
	}
	release, err := b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cmd := b.Command(ctx, path, "fetch", "--quiet", "--prune", "--no-tags", "--no-write-fetch-head",
		"--", remoteURL, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	if err := cmd.Run(); err != nil {
		return &Error{Args: []string{"fetch"}, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return nil
}

// checkBranchName validates a branch name for use in arguments.
func checkBranchName(name string) error {
	if name == "" || len(name) > 255 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "refs/") {
		return vcs.ErrBadRef
	}
	return checkRefName(name)
}

// checkRefName applies git's ref-format rules (a superset check is done by
// git itself; this prevents option injection and obvious garbage).
func checkRefName(name string) error {
	if name == "" || len(name) > 1024 || strings.HasPrefix(name, "-") {
		return vcs.ErrBadRef
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") || strings.Contains(name, "@{") ||
		strings.Contains(name, "//") || name == "@" {
		return vcs.ErrBadRef
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return vcs.ErrBadRef
		}
	}
	return nil
}

// checkPath validates a tree path: relative, no traversal, no NUL.
func checkPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.ContainsAny(p, "\x00") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return "", vcs.ErrBadPath
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "." {
		return "", nil
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
		return "", vcs.ErrBadPath
	}
	if clean != strings.TrimSuffix(p, "/") {
		// Non-canonical (e.g. "a//b", "./a"); callers should redirect.
		return "", vcs.ErrBadPath
	}
	return clean, nil
}
