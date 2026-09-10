package hg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"as215520.net/forge/internal/vcs"
)

// Repo is a Mercurial repository (the directory holding .hg). The forge
// never updates its working copy; all reads go through -r.
type Repo struct {
	b    *Backend
	path string
}

var (
	_ vcs.Repository = (*Repo)(nil)
	_ vcs.Backend    = (*Backend)(nil)
)

// Path implements vcs.Repository.
func (r *Repo) Path() string { return r.path }

func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.b.runIn(ctx, r.path, args...)
}

// Empty implements vcs.Repository.
func (r *Repo) Empty(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, "log", "-l", "1", "-T", "{node}")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) == 0, nil
}

// DefaultBranch implements vcs.Repository. Mercurial has no HEAD; the forge
// records its choice in .hg/forge-default-branch (see Backend.Init) and
// falls back to "default".
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	out, err := os.ReadFile(filepath.Join(r.path, ".hg", defaultBranchFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "default", nil
		}
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "default", nil
	}
	return name, nil
}

// refTemplate renders one ref per line: name, node, date.
const refTemplate = "{%s}\\0{node}\\0{date|rfc3339date}\\0\\n"

func parseRefs(out []byte, kind vcs.RefKind, prefix string) []vcs.Ref {
	var refs []vcs.Ref
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Split(string(line), "\x00")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		ref := vcs.Ref{Name: prefix + f[0], Kind: kind, Target: vcs.RevisionID(f[1]), Object: vcs.RevisionID(f[1])}
		if t, err := time.Parse(time.RFC3339, f[2]); err == nil {
			ref.When = t
		}
		refs = append(refs, ref)
	}
	return refs
}

// refs lists named branches (open heads only), bookmarks and global tags.
// prefix is prepended to names ("" or "refs/heads/"...).
func (r *Repo) refs(ctx context.Context, branches, tags bool, headsPrefix, tagsPrefix string) ([]vcs.Ref, error) {
	var refs []vcs.Ref
	if branches {
		out, err := r.run(ctx, "branches", "-T", strings.ReplaceAll(refTemplate, "%s", "branch"))
		if err != nil {
			return nil, err
		}
		refs = append(refs, parseRefs(out, vcs.RefBranch, headsPrefix)...)
		out, err = r.run(ctx, "bookmarks", "-T", strings.ReplaceAll(refTemplate, "%s", "bookmark"))
		if err != nil {
			return nil, err
		}
		refs = append(refs, parseRefs(out, vcs.RefBranch, headsPrefix)...)
	}
	if tags {
		// {type} is "local" for .hg/localtags entries (never pushed) and
		// empty for global tags; "tip" is synthetic.
		out, err := r.run(ctx, "tags", "-T", "{tag}\\0{node}\\0{date|rfc3339date}\\0{type}\\0\\n")
		if err != nil {
			return nil, err
		}
		for _, line := range bytes.Split(out, []byte("\n")) {
			f := strings.Split(string(line), "\x00")
			if len(f) < 4 || f[0] == "" || f[0] == "tip" || f[3] == "local" {
				continue
			}
			ref := vcs.Ref{Name: tagsPrefix + f[0], Kind: vcs.RefTag, Target: vcs.RevisionID(f[1]), Object: vcs.RevisionID(f[1])}
			if t, err := time.Parse(time.RFC3339, f[2]); err == nil {
				ref.When = t
			}
			refs = append(refs, ref)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].When.After(refs[j].When) })
	return refs, nil
}

// Refs implements vcs.Repository. Named branch heads and bookmarks are both
// RefBranch; tags exclude "tip" and local tags.
func (r *Repo) Refs(ctx context.Context) ([]vcs.Ref, error) {
	return r.refs(ctx, true, true, "", "")
}

// one runs `hg log -r <revset> -l 1 -T {node}` and returns the node or
// ErrNotFound. Revsets are built from validated parts; user strings never
// reach the parser unquoted.
func (r *Repo) one(ctx context.Context, revset string) (vcs.RevisionID, error) {
	out, err := r.run(ctx, "log", "-r", revset, "-l", "1", "-T", "{node}")
	if err != nil {
		return "", notFoundOr(err)
	}
	id := strings.TrimSpace(string(out))
	if len(id) != 40 {
		return "", vcs.ErrNotFound
	}
	return vcs.RevisionID(id), nil
}

// Resolve implements vcs.Repository. Bookmarks, tags and named branches
// (their tip-most head) win over hex prefixes, as branches win over ids in
// the git adapter.
func (r *Repo) Resolve(ctx context.Context, ref string) (vcs.RevisionID, error) {
	if err := checkName(ref); err != nil {
		return "", err
	}
	if len(ref) == 40 && isHex(ref) {
		return r.one(ctx, revsetID(vcs.RevisionID(ref)))
	}
	q := "\"" + ref + "\""
	cands := []string{
		"present(bookmark(" + q + "))",
		"present(tag(" + q + "))",
		"present(max(branch(" + q + ")))",
	}
	if isHex(ref) {
		cands = append(cands, "present(id("+q+"))")
	}
	for _, c := range cands {
		id, err := r.one(ctx, c)
		if err == nil {
			return id, nil
		}
		if err != vcs.ErrNotFound {
			return "", err
		}
	}
	return "", vcs.ErrNotFound
}

// logTemplate encodes one revision per record: fields separated by \x00 and
// records by \x01 so multi-line descriptions are unambiguous. Mercurial has
// a single author/date; committer mirrors it.
const logTemplate = "{node}\\0{p1node} {p2node}\\0{author|person}\\0{author|email}\\0{date|rfc3339date}\\0{branch}\\0{desc}\\x01"

const nullNode = "0000000000000000000000000000000000000000"

func parseRevisions(out []byte) ([]*vcs.Revision, error) {
	var revs []*vcs.Revision
	for _, rec := range bytes.Split(out, []byte("\x01")) {
		if len(rec) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\x00", 7)
		if len(f) != 7 {
			return nil, vcs.ErrUnexpected
		}
		subject, body, _ := strings.Cut(f[6], "\n")
		rev := &vcs.Revision{
			ID:      vcs.RevisionID(f[0]),
			Subject: strings.TrimSpace(subject),
			Body:    strings.TrimSpace(body),
			// Mercurial has no tree objects; expose the named branch here so
			// callers see where the changeset lives.
			Tree: f[5],
		}
		for _, p := range strings.Fields(f[1]) {
			if p != nullNode {
				rev.Parents = append(rev.Parents, vcs.RevisionID(p))
			}
		}
		rev.Author = vcs.Signature{Name: f[2], Email: f[3]}
		rev.Author.When, _ = time.Parse(time.RFC3339, f[4])
		rev.Committer = rev.Author
		revs = append(revs, rev)
	}
	return revs, nil
}

// Revision implements vcs.Repository.
func (r *Repo) Revision(ctx context.Context, id vcs.RevisionID) (*vcs.Revision, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "log", "-r", revsetID(id), "-l", "1", "-T", logTemplate)
	if err != nil {
		return nil, notFoundOr(err)
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

// Log implements vcs.Repository: limit(reverse(ancestors(id)) [and
// file("path:P")], N, skip).
func (r *Repo) Log(ctx context.Context, id vcs.RevisionID, opts vcs.LogOptions) ([]*vcs.Revision, error) {
	if err := checkID(id); err != nil {
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
	skip := opts.Skip
	if skip < 0 {
		skip = 0
	}
	set := "reverse(ancestors(" + revsetID(id) + "))"
	if path != "" {
		set += " and " + revsetPath(path)
	}
	revset := "limit(" + set + ", " + strconv.Itoa(limit) + ", " + strconv.Itoa(skip) + ")"
	if _, err := r.one(ctx, revsetID(id)); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "log", "-r", revset, "-T", logTemplate)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return parseRevisions(out)
}

// manifestTemplate lists every file of a revision with its filenode, mode,
// flags ("x" executable, "l" symlink) and size.
const manifestTemplate = "{path}\\0{hash}\\0{mode}\\0{flags}\\0{size}\\0\\n"

type manifestEntry struct {
	path, hash, mode, flags string
	size                    int64
}

func (r *Repo) manifest(ctx context.Context, id vcs.RevisionID) ([]manifestEntry, error) {
	out, err := r.run(ctx, "manifest", "-r", revsetID(id), "-T", manifestTemplate)
	if err != nil {
		return nil, notFoundOr(err)
	}
	var entries []manifestEntry
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Split(string(line), "\x00")
		if len(f) < 5 || f[0] == "" {
			continue
		}
		e := manifestEntry{path: f[0], hash: f[1], mode: f[2], flags: f[3]}
		e.size, _ = strconv.ParseInt(f[4], 10, 64)
		entries = append(entries, e)
	}
	return entries, nil
}

// Tree implements vcs.Repository. Mercurial has no tree objects: the
// one-level listing is derived from the manifest; directories are
// synthesised with an empty ID and Size -1.
func (r *Repo) Tree(ctx context.Context, id vcs.RevisionID, path string) ([]vcs.TreeEntry, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	all, err := r.manifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		// Either the revision is unknown or the tree is empty.
		if _, err := r.one(ctx, revsetID(id)); err != nil {
			return nil, err
		}
		if path != "" {
			return nil, vcs.ErrNotFound
		}
		return nil, nil
	}
	prefix := ""
	if path != "" {
		prefix = path + "/"
	}
	var entries []vcs.TreeEntry
	seen := map[string]bool{}
	for _, m := range all {
		if m.path == path {
			return nil, vcs.ErrNotDir
		}
		if !strings.HasPrefix(m.path, prefix) {
			continue
		}
		rest := m.path[len(prefix):]
		name, _, isDir := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		e := vcs.TreeEntry{Name: name, Size: -1}
		switch {
		case isDir:
			e.Kind, e.Mode = vcs.EntryDir, "040000"
		case m.flags == "l":
			e.Kind, e.Mode, e.ID, e.Size = vcs.EntrySymlink, "120000", m.hash, m.size
		case m.flags == "x":
			e.Kind, e.Mode, e.ID, e.Size = vcs.EntryExecutable, "100755", m.hash, m.size
		default:
			e.Kind, e.Mode, e.ID, e.Size = vcs.EntryFile, "100644", m.hash, m.size
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 && path != "" {
		return nil, vcs.ErrNotFound
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Kind == vcs.EntryDir) != (entries[j].Kind == vcs.EntryDir) {
			return entries[i].Kind == vcs.EntryDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Blob implements vcs.Repository. `hg cat` accepts patterns and would print
// every file under a directory, so the path is first looked up with
// `hg files`; the stream is then bounded to the recorded size.
func (r *Repo) Blob(ctx context.Context, id vcs.RevisionID, path string) (*vcs.Blob, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, vcs.ErrIsDir
	}
	out, err := r.run(ctx, "files", "-r", revsetID(id), "-T", "{path}\\0{size}\\0\\n", "--", "path:"+path)
	if err != nil {
		if e := notFoundOr(err); e == vcs.ErrNotFound {
			return nil, e
		}
		// `hg files` exits 1 when nothing matched.
		var he *Error
		if errors.As(err, &he) && he.ExitCode() == 1 {
			if _, err := r.one(ctx, revsetID(id)); err != nil {
				return nil, err
			}
			return nil, vcs.ErrNotFound
		}
		return nil, err
	}
	var size int64 = -1
	matched := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Split(string(line), "\x00")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		matched++
		if f[0] == path {
			size, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	if matched == 0 {
		return nil, vcs.ErrNotFound
	}
	if size < 0 {
		return nil, vcs.ErrIsDir
	}
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, r.b.opts.Timeout)
	cmd := r.b.Command(cctx, r.path, "cat", "-r", revsetID(id), "--", "path:"+path)
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
	return &vcs.Blob{ID: string(id) + ":" + path, Size: size, Reader: &blobReader{r: io.LimitReader(bufio.NewReader(stdout), size), done: cleanup}}, nil
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

// Diff implements vcs.Repository: `hg diff -c <id> --git` (the change
// against the first parent, or everything for a root changeset).
func (r *Repo) Diff(ctx context.Context, id vcs.RevisionID, maxBytes int64) (*vcs.Diff, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	if _, err := r.one(ctx, revsetID(id)); err != nil {
		return nil, err
	}
	return r.diff(ctx, maxBytes, "diff", "--git", "-c", revsetID(id))
}

// DiffRange implements vcs.Repository: `hg diff -r base -r head --git`.
func (r *Repo) DiffRange(ctx context.Context, base, head vcs.RevisionID, maxBytes int64) (*vcs.Diff, error) {
	return r.DiffPath(ctx, base, head, "", maxBytes)
}

// diff streams a git-format patch, computing per-file statistics over the
// whole output while retaining at most maxBytes of text.
func (r *Repo) diff(ctx context.Context, maxBytes int64, args ...string) (*vcs.Diff, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	d := &vcs.Diff{}
	var text bytes.Buffer
	err := r.b.stream(ctx, r.path, args, func(rd io.Reader) (bool, error) {
		br := bufio.NewReaderSize(rd, 64<<10)
		var st *vcs.DiffStat
		inHunk := false
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if !d.Truncated {
					if int64(text.Len()+len(line)) > maxBytes {
						d.Truncated = true
					} else {
						text.Write(line)
					}
				}
				s := string(bytes.TrimRight(line, "\n"))
				switch {
				case strings.HasPrefix(s, "diff --git "):
					inHunk = false
					d.Stats = append(d.Stats, vcs.DiffStat{})
					st = &d.Stats[len(d.Stats)-1]
					old, cur, ok := strings.Cut(strings.TrimPrefix(s, "diff --git a/"), " b/")
					if ok {
						st.Path = cur
						if old != cur {
							st.OldPath = old
						}
					}
				case st == nil:
				case inHunk:
					if strings.HasPrefix(s, "+") {
						st.Added++
					} else if strings.HasPrefix(s, "-") {
						st.Deleted++
					}
				case strings.HasPrefix(s, "@@"):
					inHunk = true
				case strings.HasPrefix(s, "rename from "), strings.HasPrefix(s, "copy from "):
					st.OldPath = s[strings.Index(s, " from ")+6:]
				case strings.HasPrefix(s, "rename to "), strings.HasPrefix(s, "copy to "):
					st.Path = s[strings.Index(s, " to ")+4:]
				case strings.HasPrefix(s, "GIT binary patch"), strings.HasPrefix(s, "Binary file "):
					st.Binary = true
				}
			}
			if err == io.EOF {
				return false, nil
			}
			if err != nil {
				return false, err
			}
		}
	})
	if err != nil {
		return nil, notFoundOr(err)
	}
	patch := text.String()
	if d.Truncated {
		if i := strings.LastIndexByte(patch, '\n'); i > 0 {
			patch = patch[:i+1]
		}
	}
	d.Patch = patch
	return d, nil
}

// Size implements vcs.Repository: bytes under .hg (store, caches, hgrc).
func (r *Repo) Size(ctx context.Context) (int64, error) {
	var total int64
	err := filepath.WalkDir(filepath.Join(r.path, ".hg"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// Check implements vcs.Repository: `hg verify -q`.
func (r *Repo) Check(ctx context.Context) error {
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cmd := r.b.Command(ctx, r.path, "verify", "-q")
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, max: 64 << 10}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return &Error{Args: []string{"verify"}, Stderr: strings.TrimSpace(out.String()), Err: err}
	}
	return nil
}
