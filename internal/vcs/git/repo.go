package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"as215520.net/forge/internal/vcs"
)

// Repo is a bare git repository.
type Repo struct {
	b    *Backend
	path string
}

// Path implements vcs.Repository.
func (r *Repo) Path() string { return r.path }

func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.b.runIn(ctx, r.path, args...)
}

// Empty implements vcs.Repository.
func (r *Repo) Empty(ctx context.Context) (bool, error) {
	_, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err == nil {
		return false, nil
	}
	var ge *Error
	if errors.As(err, &ge) && ge.ExitCode() == 1 {
		// HEAD is unborn; check whether any ref exists at all.
		out, err := r.run(ctx, "for-each-ref", "--count=1", "--format=%(refname)")
		if err != nil {
			return false, err
		}
		return len(bytes.TrimSpace(out)) == 0, nil
	}
	return false, err
}

// DefaultBranch implements vcs.Repository.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Refs implements vcs.Repository.
func (r *Repo) Refs(ctx context.Context) ([]vcs.Ref, error) {
	const format = "%(refname)%00%(objecttype)%00%(objectname)%00%(*objectname)%00%(creatordate:iso-strict)%00%(contents:subject)%00"
	out, err := r.run(ctx, "for-each-ref", "--format="+format, "--sort=-creatordate", "refs/heads/", "refs/tags/")
	if err != nil {
		return nil, err
	}
	var refs []vcs.Ref
	for _, rec := range bytes.Split(out, []byte("\x00\n")) {
		f := strings.Split(string(rec), "\x00")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		ref := vcs.Ref{Object: vcs.RevisionID(f[2]), Target: vcs.RevisionID(f[2])}
		switch {
		case strings.HasPrefix(f[0], "refs/heads/"):
			ref.Name, ref.Kind = strings.TrimPrefix(f[0], "refs/heads/"), vcs.RefBranch
		case strings.HasPrefix(f[0], "refs/tags/"):
			ref.Name, ref.Kind = strings.TrimPrefix(f[0], "refs/tags/"), vcs.RefTag
		default:
			ref.Name, ref.Kind = f[0], vcs.RefOther
		}
		if f[1] == "tag" {
			ref.Target = vcs.RevisionID(f[3])
			ref.Message = f[5]
		}
		if t, err := time.Parse(time.RFC3339, f[4]); err == nil {
			ref.When = t
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// Resolve implements vcs.Repository.
func (r *Repo) Resolve(ctx context.Context, ref string) (vcs.RevisionID, error) {
	if err := checkRefName(ref); err != nil {
		return "", err
	}
	// Prefer branches and tags over raw ids to avoid ambiguity, then fall
	// back to an object id. --end-of-options guards against option injection.
	for _, cand := range []string{"refs/heads/" + ref, "refs/tags/" + ref, ref} {
		out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", cand+"^{commit}")
		if err == nil {
			return vcs.RevisionID(strings.TrimSpace(string(out))), nil
		}
		var ge *Error
		if !errors.As(err, &ge) || ge.ExitCode() != 1 {
			return "", err
		}
	}
	return "", vcs.ErrNotFound
}

// logFormat encodes one revision per record. Fields are separated by \x00
// and records by \x01 so that multi-line bodies are unambiguous.
const logFormat = "%H%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%T%x00%s%x00%b%x01"

func parseRevisions(out []byte) ([]*vcs.Revision, error) {
	var revs []*vcs.Revision
	for _, rec := range bytes.Split(out, []byte("\x01")) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\x00", 11)
		if len(f) != 11 {
			return nil, vcs.ErrUnexpected
		}
		rev := &vcs.Revision{
			ID:      vcs.RevisionID(f[0]),
			Subject: f[9],
			Body:    strings.TrimRight(f[10], "\n"),
			Tree:    f[8],
		}
		for _, p := range strings.Fields(f[1]) {
			rev.Parents = append(rev.Parents, vcs.RevisionID(p))
		}
		rev.Author = vcs.Signature{Name: f[2], Email: f[3]}
		rev.Author.When, _ = time.Parse(time.RFC3339, f[4])
		rev.Committer = vcs.Signature{Name: f[5], Email: f[6]}
		rev.Committer.When, _ = time.Parse(time.RFC3339, f[7])
		revs = append(revs, rev)
	}
	return revs, nil
}

// Revision implements vcs.Repository.
func (r *Repo) Revision(ctx context.Context, id vcs.RevisionID) (*vcs.Revision, error) {
	if err := checkRefName(string(id)); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "log", "-n", "1", "--format="+logFormat, "--end-of-options", string(id), "--")
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && ge.ExitCode() == 128 {
			return nil, vcs.ErrNotFound
		}
		return nil, err
	}
	revs, err := parseRevisions(out)
	if err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, vcs.ErrNotFound
	}
	return revs[0], nil
}

// Log implements vcs.Repository.
func (r *Repo) Log(ctx context.Context, id vcs.RevisionID, opts vcs.LogOptions) ([]*vcs.Revision, error) {
	if err := checkRefName(string(id)); err != nil {
		return nil, err
	}
	path, err := checkPath(opts.Path)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	args := []string{"log", "--format=" + logFormat, "-n", strconv.Itoa(limit)}
	if opts.Skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(opts.Skip))
	}
	args = append(args, "--end-of-options", string(id), "--")
	if path != "" {
		args = append(args, path)
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && ge.ExitCode() == 128 {
			return nil, vcs.ErrNotFound
		}
		return nil, err
	}
	return parseRevisions(out)
}

// Tree implements vcs.Repository.
func (r *Repo) Tree(ctx context.Context, id vcs.RevisionID, path string) ([]vcs.TreeEntry, error) {
	if err := checkRefName(string(id)); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	spec := string(id) + ":"
	if path != "" {
		spec += path
	}
	// Ensure the target is a tree so we can return ErrNotDir for files.
	typ, err := r.run(ctx, "cat-file", "-t", "--end-of-options", spec)
	if err != nil {
		return nil, notFoundOr(err)
	}
	if t := strings.TrimSpace(string(typ)); t != "tree" && t != "commit" {
		return nil, vcs.ErrNotDir
	}
	args := []string{"ls-tree", "-z", "-l", "--end-of-options", string(id)}
	if path != "" {
		args = append(args, "--", path+"/")
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, notFoundOr(err)
	}
	var entries []vcs.TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// <mode> SP <type> SP <object> SP <size> TAB <name>
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, vcs.ErrUnexpected
		}
		f := strings.Fields(string(meta))
		if len(f) != 4 {
			return nil, vcs.ErrUnexpected
		}
		e := vcs.TreeEntry{Mode: f[0], ID: f[2], Size: -1}
		n := string(name)
		if i := strings.LastIndexByte(n, '/'); i >= 0 {
			n = n[i+1:]
		}
		e.Name = n
		switch {
		case f[1] == "tree":
			e.Kind = vcs.EntryDir
		case f[1] == "commit":
			e.Kind = vcs.EntrySubmodule
		case f[0] == "120000":
			e.Kind = vcs.EntrySymlink
		case f[0] == "100755":
			e.Kind = vcs.EntryExecutable
		default:
			e.Kind = vcs.EntryFile
		}
		if f[3] != "-" {
			e.Size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func notFoundOr(err error) error {
	var ge *Error
	if errors.As(err, &ge) && ge.ExitCode() == 128 {
		return vcs.ErrNotFound
	}
	return err
}

// Blob implements vcs.Repository. It streams the object through
// `git cat-file --batch`; the returned reader is bounded to the object size
// and closing it terminates the subprocess.
func (r *Repo) Blob(ctx context.Context, id vcs.RevisionID, path string) (*vcs.Blob, error) {
	if err := checkRefName(string(id)); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, vcs.ErrIsDir
	}
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, r.b.opts.Timeout)
	cmd := r.b.Command(cctx, r.path, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(string(id) + ":" + path + "\n")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		release()
		return nil, err
	}
	cleanup := func() {
		cancel()
		_ = cmd.Wait()
		release()
	}
	br := bufio.NewReader(stdout)
	header, err := br.ReadString('\n')
	if err != nil {
		cleanup()
		return nil, vcs.ErrNotFound
	}
	f := strings.Fields(header)
	if len(f) == 2 && f[1] == "missing" {
		cleanup()
		return nil, vcs.ErrNotFound
	}
	if len(f) != 3 {
		cleanup()
		return nil, vcs.ErrUnexpected
	}
	if f[1] != "blob" {
		cleanup()
		if f[1] == "tree" {
			return nil, vcs.ErrIsDir
		}
		return nil, vcs.ErrNotFound
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		cleanup()
		return nil, vcs.ErrUnexpected
	}
	return &vcs.Blob{ID: f[0], Size: size, Reader: &blobReader{r: io.LimitReader(br, size), done: cleanup}}, nil
}

type blobReader struct {
	r    io.Reader
	done func()
	once bool
}

func (b *blobReader) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *blobReader) Close() error {
	if !b.once {
		b.once = true
		b.done()
	}
	return nil
}

// Diff implements vcs.Repository.
func (r *Repo) Diff(ctx context.Context, id vcs.RevisionID, maxBytes int64) (*vcs.Diff, error) {
	rev, err := r.Revision(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rev.Parents) == 0 {
		return r.diff(ctx, maxBytes, string(rev.ID))
	}
	return r.diff(ctx, maxBytes, string(rev.Parents[0]), string(rev.ID))
}

// DiffRange implements vcs.Repository.
func (r *Repo) DiffRange(ctx context.Context, base, head vcs.RevisionID, maxBytes int64) (*vcs.Diff, error) {
	if err := checkRefName(string(base)); err != nil {
		return nil, err
	}
	if err := checkRefName(string(head)); err != nil {
		return nil, err
	}
	return r.diff(ctx, maxBytes, string(base), string(head))
}

func (r *Repo) diff(ctx context.Context, maxBytes int64, revs ...string) (*vcs.Diff, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	args := append([]string{"diff-tree", "-r", "-M", "--root", "--no-color", "--numstat", "-z", "--end-of-options"}, revs...)
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, notFoundOr(err)
	}
	d := &vcs.Diff{}
	// diff-tree without --stdin prints the commit id line first for single-rev
	// forms; with -z, records: "<added>\t<deleted>\t<path>\0" or for renames
	// "<added>\t<deleted>\t\0<old>\0<new>\0".
	rest := out
	if i := bytes.IndexByte(rest, '\x00'); i > 0 && !bytes.Contains(rest[:i], []byte{'\t'}) {
		rest = rest[i+1:] // leading commit id
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			break
		}
		rec := string(rest[:i])
		rest = rest[i+1:]
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			return nil, vcs.ErrUnexpected
		}
		st := vcs.DiffStat{}
		if f[0] == "-" {
			st.Binary = true
		} else {
			st.Added, _ = strconv.Atoi(f[0])
			st.Deleted, _ = strconv.Atoi(f[1])
		}
		if f[2] == "" {
			// rename: two more NUL-terminated fields
			j := bytes.IndexByte(rest, 0)
			k := -1
			if j >= 0 {
				k = bytes.IndexByte(rest[j+1:], 0)
			}
			if j < 0 || k < 0 {
				return nil, vcs.ErrUnexpected
			}
			st.OldPath = string(rest[:j])
			st.Path = string(rest[j+1 : j+1+k])
			rest = rest[j+1+k+1:]
		} else {
			st.Path = f[2]
		}
		d.Stats = append(d.Stats, st)
	}
	args = append([]string{"diff-tree", "-r", "-M", "--root", "-p", "--no-color", "--end-of-options"}, revs...)
	patch, err := r.runPatch(ctx, maxBytes, args...)
	if err != nil {
		return nil, notFoundOr(err)
	}
	d.Patch = patch.text
	d.Truncated = patch.truncated
	return d, nil
}

type patchOut struct {
	text      string
	truncated bool
}

// runPatch runs a diff command and reads at most maxBytes of output,
// terminating the process early if the patch is larger.
func (r *Repo) runPatch(ctx context.Context, maxBytes int64, args ...string) (patchOut, error) {
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return patchOut{}, err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, r.b.opts.Timeout)
	defer cancel()
	cmd := r.b.Command(cctx, r.path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return patchOut{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	if err := cmd.Start(); err != nil {
		return patchOut{}, err
	}
	buf, rerr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	truncated := int64(len(buf)) > maxBytes
	if truncated {
		buf = buf[:maxBytes]
		cancel()
	}
	werr := cmd.Wait()
	if rerr != nil {
		return patchOut{}, rerr
	}
	if werr != nil && !truncated {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return patchOut{}, &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: werr}
		}
		return patchOut{}, werr
	}
	// For single-rev diff-tree the first line is the commit id; drop it.
	text := string(buf)
	if len(args) > 0 && strings.HasPrefix(text, args[len(args)-1]+"\n") {
		text = strings.TrimPrefix(text, args[len(args)-1]+"\n")
	}
	if truncated {
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i+1]
		}
	}
	return patchOut{text: text, truncated: truncated}, nil
}

// Size implements vcs.Repository.
func (r *Repo) Size(ctx context.Context) (int64, error) {
	out, err := r.run(ctx, "count-objects", "-v")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(l, ": ")
		if !ok {
			continue
		}
		if k == "size" || k == "size-pack" || k == "size-garbage" {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			total += n * 1024
		}
	}
	return total, nil
}

// Check implements vcs.Repository.
func (r *Repo) Check(ctx context.Context) error {
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cmd := r.b.Command(ctx, r.path, "fsck", "--no-dangling", "--no-progress")
	var stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stderr, max: 64 << 10}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fsck: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
