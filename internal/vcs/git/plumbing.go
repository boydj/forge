package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"as215520.net/forge/internal/vcs"
)

// This file holds the plumbing needed by the change-review model
// (ADR 0012 §13): merge bases, ranges, merge-tree/commit-tree and atomic
// ref transactions. Everything runs through the hardened runner; ids and
// ref names are validated before they reach the command line.

// zeroOID is the all-zero object id (SHA-1) git uses for "absent".
const zeroOID = "0000000000000000000000000000000000000000"

func checkIDs(ids ...vcs.RevisionID) error {
	for _, id := range ids {
		if err := checkRefName(string(id)); err != nil {
			return err
		}
	}
	return nil
}

// exitCode returns git's exit status for err, or -1.
func exitCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.ExitCode()
	}
	return -1
}

// MergeBase implements vcs.Repository.
func (r *Repo) MergeBase(ctx context.Context, a, b vcs.RevisionID) (vcs.RevisionID, error) {
	if err := checkIDs(a, b); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "merge-base", "--end-of-options", string(a), string(b))
	if err != nil {
		// 1: no common ancestor; 128: unknown revision.
		if c := exitCode(err); c == 1 || c == 128 {
			return "", vcs.ErrNotFound
		}
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", vcs.ErrNotFound
	}
	return vcs.RevisionID(id), nil
}

// IsAncestor implements vcs.Repository.
func (r *Repo) IsAncestor(ctx context.Context, a, b vcs.RevisionID) (bool, error) {
	if err := checkIDs(a, b); err != nil {
		return false, err
	}
	_, err := r.run(ctx, "merge-base", "--is-ancestor", "--end-of-options", string(a), string(b))
	if err == nil {
		return true, nil
	}
	switch exitCode(err) {
	case 1:
		return false, nil
	case 128:
		return false, vcs.ErrNotFound
	}
	return false, err
}

// CountCommits implements vcs.Repository.
func (r *Repo) CountCommits(ctx context.Context, base, head vcs.RevisionID) (int, error) {
	if err := checkIDs(base, head); err != nil {
		return 0, err
	}
	out, err := r.run(ctx, "rev-list", "--count", "--end-of-options", string(base)+".."+string(head), "--")
	if err != nil {
		return 0, notFoundOr(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, vcs.ErrUnexpected
	}
	return n, nil
}

// ListCommits implements vcs.Repository.
func (r *Repo) ListCommits(ctx context.Context, base, head vcs.RevisionID, limit int) ([]*vcs.Revision, error) {
	if err := checkIDs(base, head); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	out, err := r.run(ctx, "log", "--format="+logFormat, "--reverse", "-n", strconv.Itoa(limit),
		"--end-of-options", string(base)+".."+string(head), "--")
	if err != nil {
		return nil, notFoundOr(err)
	}
	return parseRevisions(out)
}

// RangeDiff implements vcs.Repository.
func (r *Repo) RangeDiff(ctx context.Context, base1, head1, base2, head2 vcs.RevisionID, maxBytes int64) (string, bool, error) {
	if err := checkIDs(base1, head1, base2, head2); err != nil {
		return "", false, err
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	// range-diff forwards unknown options to diff, so --end-of-options is
	// not usable here; the validated ids cannot start with "-".
	p, err := r.runPatch(ctx, maxBytes, "range-diff", "--no-color", "--no-notes",
		string(base1)+".."+string(head1), string(base2)+".."+string(head2))
	if err != nil {
		return "", false, notFoundOr(err)
	}
	return p.text, p.truncated, nil
}

// FormatPatch implements vcs.Repository.
func (r *Repo) FormatPatch(ctx context.Context, base, head vcs.RevisionID, maxBytes int64, w io.Writer) error {
	if err := checkIDs(base, head); err != nil {
		return err
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	args := []string{"format-patch", "--stdout", "--no-color", "--end-of-options", string(base) + ".." + string(head)}
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, r.b.opts.Timeout)
	defer cancel()
	cmd := r.b.Command(cctx, r.path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, cerr := io.Copy(w, io.LimitReader(stdout, maxBytes))
	truncated := false
	if cerr == nil {
		// Anything left means the output exceeded maxBytes.
		var probe [1]byte
		if n, _ := stdout.Read(probe[:]); n > 0 {
			truncated = true
			cancel()
		}
	} else {
		cancel()
	}
	werr := cmd.Wait()
	if cerr != nil {
		return cerr
	}
	if truncated {
		return vcs.ErrTooLarge
	}
	if werr != nil {
		if cctx.Err() != nil {
			return vcs.ErrTimeout
		}
		return notFoundOr(&Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: werr})
	}
	return nil
}

// DiffPath implements vcs.Repository.
func (r *Repo) DiffPath(ctx context.Context, base, head vcs.RevisionID, path string, maxBytes int64) (*vcs.Diff, error) {
	if err := checkIDs(base, head); err != nil {
		return nil, err
	}
	path, err := checkPath(path)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return r.diff(ctx, maxBytes, string(base), string(head))
	}
	// diff appends its arguments after --end-of-options, so the pathspec
	// separator and path ride along as trailing "revisions".
	return r.diff(ctx, maxBytes, string(base), string(head), "--", path)
}

// MergeTree implements vcs.Repository.
func (r *Repo) MergeTree(ctx context.Context, ours, theirs vcs.RevisionID) (string, []string, error) {
	if err := checkIDs(ours, theirs); err != nil {
		return "", nil, err
	}
	out, err := r.run(ctx, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z",
		"--end-of-options", string(ours), string(theirs))
	if err != nil && exitCode(err) != 1 {
		return "", nil, notFoundOr(err)
	}
	// Output: <tree>NUL then, when conflicted (exit 1), one path per NUL.
	// An unresolvable revision also exits 1, with nothing on stdout.
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	tree := strings.TrimSpace(fields[0])
	if len(tree) < 40 {
		if err != nil {
			return "", nil, vcs.ErrNotFound
		}
		return "", nil, vcs.ErrUnexpected
	}
	if err == nil {
		return tree, nil, nil
	}
	var conflicts []string
	for _, p := range fields[1:] {
		if p != "" {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) == 0 {
		return "", nil, vcs.ErrUnexpected
	}
	return tree, conflicts, nil
}

// gitDate renders t in git's internal "@<unix> <tz>" form.
func gitDate(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return fmt.Sprintf("@%d %s", t.Unix(), t.Format("-0700"))
}

// CommitTree implements vcs.Repository.
func (r *Repo) CommitTree(ctx context.Context, tree string, parents []vcs.RevisionID, author, committer vcs.Signature, message string) (vcs.RevisionID, error) {
	if err := checkRefName(tree); err != nil {
		return "", err
	}
	if err := checkIDs(parents...); err != nil {
		return "", err
	}
	if committer.Name == "" {
		committer = author
	}
	env := r.b.Env(
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+gitDate(author.When),
		"GIT_COMMITTER_NAME="+committer.Name,
		"GIT_COMMITTER_EMAIL="+committer.Email,
		"GIT_COMMITTER_DATE="+gitDate(committer.When),
	)
	// commit-tree parses its arguments by hand and has no --end-of-options;
	// tree and parents are validated above and cannot start with "-".
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", string(p))
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	out, err := r.runEnv(ctx, env, []byte(message), args...)
	if err != nil {
		return "", notFoundOr(err)
	}
	id := strings.TrimSpace(string(out))
	if len(id) < 40 {
		return "", vcs.ErrUnexpected
	}
	return vcs.RevisionID(id), nil
}

// UpdateRefs implements vcs.Repository.
func (r *Repo) UpdateRefs(ctx context.Context, updates []vcs.RefUpdate, reason string) error {
	if len(updates) == 0 {
		return nil
	}
	var in bytes.Buffer
	in.WriteString("start\n")
	for _, u := range updates {
		if err := checkRefName(u.Ref); err != nil {
			return err
		}
		if !strings.HasPrefix(u.Ref, "refs/") {
			return vcs.ErrBadRef
		}
		if u.New != "" {
			if err := checkRefName(string(u.New)); err != nil {
				return err
			}
		}
		if u.Old != "" {
			if err := checkRefName(string(u.Old)); err != nil {
				return err
			}
		}
		switch {
		case u.New == "" && u.Old == "":
			fmt.Fprintf(&in, "delete %s\n", u.Ref)
		case u.New == "":
			fmt.Fprintf(&in, "delete %s %s\n", u.Ref, u.Old)
		case u.Old == "" || u.Old == zeroOID:
			fmt.Fprintf(&in, "create %s %s\n", u.Ref, u.New)
		default:
			fmt.Fprintf(&in, "update %s %s %s\n", u.Ref, u.New, u.Old)
		}
	}
	in.WriteString("prepare\ncommit\n")
	args := []string{"update-ref", "--no-deref", "--stdin"}
	if reason != "" {
		args = append(args, "-m", strings.ReplaceAll(reason, "\n", " "))
	}
	_, err := r.b.runInput(ctx, r.path, in.Bytes(), args...)
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "cannot lock ref") {
			return vcs.ErrConflict
		}
		return err
	}
	return nil
}

// RefsMatching implements vcs.Repository.
func (r *Repo) RefsMatching(ctx context.Context, prefix string) ([]vcs.Ref, error) {
	if err := checkRefName(strings.TrimSuffix(prefix, "/")); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(prefix, "refs/") {
		return nil, vcs.ErrBadRef
	}
	const format = "%(refname)%00%(objecttype)%00%(objectname)%00%(*objectname)%00%(creatordate:iso-strict)%00%(contents:subject)%00"
	out, err := r.run(ctx, "for-each-ref", "--format="+format, "--end-of-options", prefix)
	if err != nil {
		return nil, err
	}
	var refs []vcs.Ref
	for _, rec := range bytes.Split(out, []byte("\x00\n")) {
		f := strings.Split(string(rec), "\x00")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		ref := vcs.Ref{Name: f[0], Kind: vcs.RefOther, Object: vcs.RevisionID(f[2]), Target: vcs.RevisionID(f[2])}
		switch {
		case strings.HasPrefix(f[0], "refs/heads/"):
			ref.Kind = vcs.RefBranch
		case strings.HasPrefix(f[0], "refs/tags/"):
			ref.Kind = vcs.RefTag
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

// ObjectType implements vcs.Repository.
func (r *Repo) ObjectType(ctx context.Context, id vcs.RevisionID) (string, error) {
	if err := checkIDs(id); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "cat-file", "-t", "--end-of-options", string(id))
	if err != nil {
		return "", notFoundOr(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runEnv is runInput with a custom environment (identity variables for
// commit-tree). It mirrors the runner's limits and error mapping.
func (r *Repo) runEnv(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
	release, err := r.b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, r.b.opts.Timeout)
	defer cancel()
	cmd := r.b.Command(ctx, r.path, args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout limitedBuffer
	stdout.max = r.b.opts.MaxOutputBytes
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
