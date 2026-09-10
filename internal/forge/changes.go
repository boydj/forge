package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"as215520.net/forge/internal/hooks"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
)

// Change review model (ADR 0012): changes are created and updated by pushes
// to refs/for/<branch> and refs/changes/<n>, handled here from the
// proc-receive hook; reviews, comments, close/reopen and merge arrive over
// Titan.

var topicRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func changePath(r *store.Repo, n int64) string {
	return fmt.Sprintf("/~%s/%s/changes/%d", r.Owner, r.Name, n)
}

// pushOptions parsed from -o key=value.
type pushOptions struct {
	topic  string
	change int64
	title  string
}

func parsePushOptions(opts []string) (pushOptions, error) {
	var po pushOptions
	for _, o := range opts {
		k, v, ok := strings.Cut(o, "=")
		if !ok {
			return po, fmt.Errorf("push option %q must be key=value", o)
		}
		switch k {
		case "topic":
			if !topicRe.MatchString(v) {
				return po, fmt.Errorf("topic %q: use [a-z0-9][a-z0-9._-]{0,63}", v)
			}
			po.topic = v
		case "change":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return po, fmt.Errorf("change option must be a positive number")
			}
			po.change = n
		case "title":
			v = strings.TrimSpace(v)
			if v == "" || len(v) > MaxTitleLen {
				return po, fmt.Errorf("title option is empty or too long")
			}
			po.title = v
		default:
			return po, fmt.Errorf("unknown push option %q (known: topic, change, title)", k)
		}
	}
	return po, nil
}

// procReceive handles the proc-receive hook: exactly one refs/for/<branch>
// or refs/changes/<n> command (pre-receive enforced this).
func (f *Forge) procReceive(ctx context.Context, req *hooks.Request, r *store.Repo, u *store.User, role store.Role) *hooks.Response {
	resp := &hooks.Response{OK: true}
	fail := func(ref, reason string) *hooks.Response {
		for _, up := range req.Updates {
			resp.Results = append(resp.Results, hooks.Result{Ref: up.Ref, OK: false, Reason: reason})
		}
		resp.OK = false
		resp.Messages = append(resp.Messages, "forge: "+reason)
		return resp
	}
	if len(req.Updates) != 1 {
		return fail("", "push one change at a time")
	}
	up := req.Updates[0]
	po, err := parsePushOptions(req.PushOptions)
	if err != nil {
		return fail(up.Ref, err.Error())
	}
	writer := role == store.RoleWrite || role == store.RoleAdmin
	repo, err := f.Open(r)
	if err != nil {
		return fail(up.Ref, "repository unavailable")
	}
	if t, err := repo.ObjectType(ctx, vcs.RevisionID(up.New)); err != nil || t != "commit" {
		return fail(up.Ref, "only commits can be proposed (annotated tags are not accepted)")
	}
	tip := vcs.RevisionID(up.New)

	var ch *store.Change
	var target string
	switch {
	case strings.HasPrefix(up.Ref, "refs/for/"):
		target = strings.TrimPrefix(up.Ref, "refs/for/")
		if po.change != 0 {
			ch, err = f.Store.ChangeByNumber(ctx, r.ID, po.change)
			if err != nil {
				return fail(up.Ref, fmt.Sprintf("change %d does not exist", po.change))
			}
		} else if po.topic != "" {
			if c, err := f.Store.OpenChangeByTopic(ctx, r.ID, u.ID, target, po.topic); err == nil {
				ch = c
			}
		}
	case isChangeRef(up.Ref):
		n, _ := strconv.ParseInt(strings.TrimPrefix(up.Ref, "refs/changes/"), 10, 64)
		ch, err = f.Store.ChangeByNumber(ctx, r.ID, n)
		if err != nil {
			return fail(up.Ref, fmt.Sprintf("change %d does not exist", n))
		}
		target = ch.TargetBranch
	default:
		return fail(up.Ref, "unexpected ref "+up.Ref)
	}
	if ch != nil {
		if ch.State == "merged" {
			return fail(up.Ref, fmt.Sprintf("change %d is merged; push to refs/for/%s to start a new one", ch.Number, ch.TargetBranch))
		}
		if ch.AuthorID != u.ID && !writer {
			return fail(up.Ref, fmt.Sprintf("only the author or a repository writer may update change %d", ch.Number))
		}
		if ch.HeadRev == up.New {
			return fail(up.Ref, fmt.Sprintf("%s is already the current version of change %d", short(up.New), ch.Number))
		}
	}
	targetID, err := repo.Resolve(ctx, "refs/heads/"+target)
	if err != nil {
		return fail(up.Ref, fmt.Sprintf("target branch %q does not exist", target))
	}
	base, err := repo.MergeBase(ctx, targetID, tip)
	if err != nil {
		return fail(up.Ref, "no common history with "+target)
	}
	if anc, _ := repo.IsAncestor(ctx, tip, targetID); anc {
		return fail(up.Ref, fmt.Sprintf("nothing to review: %s is already in %s", short(up.New), target))
	}
	count, err := repo.CountCommits(ctx, base, tip)
	if err != nil {
		return fail(up.Ref, "cannot count commits")
	}
	if count == 0 {
		return fail(up.Ref, "nothing to review")
	}
	if count > f.Config.Limits.MaxChangeCommits {
		return fail(up.Ref, fmt.Sprintf("a change may contain at most %d commits", f.Config.Limits.MaxChangeCommits))
	}

	if ch == nil {
		if !writer {
			n, err := f.Store.CountOpenChangesByAuthor(ctx, r.ID, u.ID)
			if err == nil && n >= f.Config.Limits.MaxOpenChangesPerUser {
				return fail(up.Ref, "too many open changes")
			}
		}
		title, body := po.title, ""
		if title == "" {
			first, err := repo.ListCommits(ctx, base, tip, 1)
			if err != nil || len(first) == 0 {
				return fail(up.Ref, "cannot read commits")
			}
			title, body = first[0].Subject, first[0].Body
		}
		if len(title) > MaxTitleLen {
			title = title[:MaxTitleLen]
		}
		ch = &store.Change{RepoID: r.ID, AuthorID: u.ID, Title: title, Body: body, Topic: po.topic, TargetBranch: target}
	}
	// Reserve the version number before creating refs so that the ref names
	// are known; the store call below allocates the same numbers.
	nextVersion := ch.Version + 1
	nextNumber := ch.Number
	if ch.ID == 0 {
		// Peek at the next number; the transaction re-derives it and both
		// agree because pushes to one repository are serialised by git's
		// receive-pack lock and our own store transaction.
		list, err := f.Store.ListChanges(ctx, r.ID, "", 1)
		if err != nil {
			return fail(up.Ref, "database error")
		}
		nextNumber = 1
		for _, c := range list {
			if c.Number >= nextNumber {
				nextNumber = c.Number + 1
			}
		}
	}
	oldHead := vcs.RevisionID("")
	if ch.ID != 0 {
		oldHead = vcs.RevisionID(ch.HeadRev)
	}
	updates := []vcs.RefUpdate{
		{Ref: fmt.Sprintf("refs/changes/%d/v%d", nextNumber, nextVersion), New: tip},
		{Ref: fmt.Sprintf("refs/changes/%d/head", nextNumber), New: tip, Old: oldHead},
	}
	if err := repo.UpdateRefs(ctx, updates, fmt.Sprintf("forge: change %d v%d", nextNumber, nextVersion)); err != nil {
		return fail(up.Ref, "could not record the version; retry: "+err.Error())
	}
	created := ch.ID == 0
	fresh, ver, err := f.Store.NewChangeVersion(ctx, ch, up.New, string(base), count, u.ID)
	if err != nil || fresh.Number != nextNumber || ver.Number != nextVersion {
		_ = repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: updates[0].Ref, New: "", Old: tip}}, "forge: rollback")
		if oldHead == "" {
			_ = repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: updates[1].Ref, New: "", Old: tip}}, "forge: rollback")
		} else {
			_ = repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: updates[1].Ref, New: oldHead, Old: tip}}, "forge: rollback")
		}
		return fail(up.Ref, "database error recording the change")
	}
	payload, _ := json.Marshal(map[string]any{"number": fresh.Number, "version": ver.Number, "head": up.New, "base": string(base), "commits": count})
	kind := store.EventChangeUpdate
	subject := fmt.Sprintf("%s pushed v%d of change #%d in %s/%s: %s", u.Name, ver.Number, fresh.Number, r.Owner, r.Name, fresh.Title)
	if created {
		kind = store.EventChangeOpen
		subject = fmt.Sprintf("%s proposed change #%d in %s/%s: %s", u.Name, fresh.Number, r.Owner, r.Name, fresh.Title)
	}
	f.Event(ctx, kind, r, u, subject, changePath(r, fresh.Number), payload)
	resp.Results = []hooks.Result{{Ref: up.Ref, OK: true, RefName: updates[0].Ref, OldOID: zeroID, NewOID: up.New}}
	verb := "updated"
	if created {
		verb = "created"
	}
	resp.Messages = append(resp.Messages,
		fmt.Sprintf("forge: %s change %d (v%d, %s) targeting %s", verb, fresh.Number, ver.Number, plural(count, "commit"), target),
		"forge:   "+f.Config.GeminiURL(changePath(r, fresh.Number)),
		fmt.Sprintf("forge:   next version: git push origin HEAD:refs/changes/%d", fresh.Number))
	return resp
}

func short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

// LookupChange loads a change by number.
func (f *Forge) LookupChange(ctx context.Context, acc Access, number int64) (*store.Change, error) {
	ch, err := f.Store.ChangeByNumber(ctx, acc.Repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return ch, err
}

func canManageChange(u *store.User, acc Access, ch *store.Change) bool {
	return u != nil && (acc.CanWrite() || ch.AuthorID == u.ID)
}

// CommentChange adds a comment to a change.
func (f *Forge) CommentChange(ctx context.Context, u *store.User, acc Access, ch *store.Change, text string) (*store.Comment, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if body == "" {
		return nil, ErrNotAcceptable
	}
	if err := f.checkText("", body); err != nil {
		return nil, err
	}
	c, err := f.Store.AddChangeComment(ctx, acc.Repo.ID, ch.ID, u.ID, ch.Version, body)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"target_kind": "change", "number": ch.Number})
	f.Event(ctx, store.EventComment, acc.Repo, u, fmt.Sprintf("%s commented on change #%d in %s/%s: %s", u.Name, ch.Number, acc.Repo.Owner, acc.Repo.Name, ch.Title), changePath(acc.Repo, ch.Number), payload)
	return c, nil
}

// ReviewChange records a review; the first line of text is the verdict.
func (f *Forge) ReviewChange(ctx context.Context, u *store.User, acc Access, ch *store.Change, text string) (*store.Review, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	if ch.State == "merged" || ch.State == "closed" {
		return nil, ErrNotAcceptable
	}
	verdictLine, body := SplitTitleBody(text)
	verdict := strings.ToLower(strings.TrimSpace(verdictLine))
	switch verdict {
	case "approve", "approved", "lgtm", "+1":
		verdict = "approve"
	case "request-changes", "request changes", "changes-requested", "-1":
		verdict = "request-changes"
	case "comment":
	default:
		return nil, fmt.Errorf("first line must be approve, request-changes or comment: %w", ErrNotAcceptable)
	}
	counts := acc.CanWrite() && ch.AuthorID != u.ID
	if !counts && verdict != "comment" {
		verdict = "comment"
	}
	if err := f.checkText("", body); err != nil {
		return nil, err
	}
	rv, err := f.Store.AddReview(ctx, &store.Review{ChangeID: ch.ID, ReviewerID: u.ID, Verdict: verdict, Body: body, Version: ch.Version, HeadRev: ch.HeadRev, Counts: counts})
	if err != nil {
		return nil, err
	}
	if counts {
		state, err := f.Store.ComputeChangeState(ctx, ch.ID, ch.Version)
		if err == nil && state != ch.State {
			_ = f.Store.SetChangeState(ctx, ch.ID, state, u.ID)
		}
	}
	verb := map[string]string{"approve": "approved", "request-changes": "requested changes on", "comment": "reviewed"}[verdict]
	f.Event(ctx, store.EventReview, acc.Repo, u, fmt.Sprintf("%s %s change #%d in %s/%s: %s", u.Name, verb, ch.Number, acc.Repo.Owner, acc.Repo.Name, ch.Title), changePath(acc.Repo, ch.Number), nil)
	return rv, nil
}

// EditChange replaces title and body.
func (f *Forge) EditChange(ctx context.Context, u *store.User, acc Access, ch *store.Change, text string) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !canManageChange(u, acc, ch) {
		return ErrForbidden
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	title, body := SplitTitleBody(text)
	if title == "" {
		return ErrNotAcceptable
	}
	if err := f.checkText(title, body); err != nil {
		return err
	}
	if err := f.Store.UpdateChangeText(ctx, ch.ID, title, body); err != nil {
		return err
	}
	f.Event(ctx, store.EventChangeUpdate, acc.Repo, u, fmt.Sprintf("%s edited change #%d in %s/%s: %s", u.Name, ch.Number, acc.Repo.Owner, acc.Repo.Name, title), changePath(acc.Repo, ch.Number), nil)
	return nil
}

// CloseChange closes (or reopens) a change; reason becomes a comment.
func (f *Forge) CloseChange(ctx context.Context, u *store.User, acc Access, ch *store.Change, closed bool, reason string) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !canManageChange(u, acc, ch) {
		return ErrForbidden
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	if ch.State == "merged" {
		return ErrNotAcceptable
	}
	if !closed && acc.Repo.Archived {
		return ErrArchived
	}
	if closed {
		if ch.State == "closed" {
			return nil
		}
		if err := f.Store.SetChangeState(ctx, ch.ID, "closed", u.ID); err != nil {
			return err
		}
		f.Event(ctx, store.EventChangeClose, acc.Repo, u, fmt.Sprintf("%s closed change #%d in %s/%s: %s", u.Name, ch.Number, acc.Repo.Owner, acc.Repo.Name, ch.Title), changePath(acc.Repo, ch.Number), nil)
	} else {
		if ch.State != "closed" {
			return nil
		}
		repo, err := f.Open(acc.Repo)
		if err != nil {
			return err
		}
		if _, err := repo.Resolve(ctx, "refs/heads/"+ch.TargetBranch); err != nil {
			return fmt.Errorf("target branch %s no longer exists: %w", ch.TargetBranch, ErrNotAcceptable)
		}
		state, err := f.Store.ComputeChangeState(ctx, ch.ID, ch.Version)
		if err != nil {
			return err
		}
		if err := f.Store.SetChangeState(ctx, ch.ID, state, u.ID); err != nil {
			return err
		}
		f.Event(ctx, store.EventChangeUpdate, acc.Repo, u, fmt.Sprintf("%s reopened change #%d in %s/%s: %s", u.Name, ch.Number, acc.Repo.Owner, acc.Repo.Name, ch.Title), changePath(acc.Repo, ch.Number), nil)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		_, _ = f.Store.AddChangeComment(ctx, acc.Repo.ID, ch.ID, u.ID, ch.Version, reason)
	}
	return nil
}

// MergePreview describes what merging would do.
type MergePreview struct {
	FastForward bool
	Conflicts   []string
	Tree        string
	TargetHead  vcs.RevisionID
	Behind      int
	Already     bool
	Missing     bool
}

// PreviewMerge computes the merge outcome against the live target branch.
func (f *Forge) PreviewMerge(ctx context.Context, acc Access, ch *store.Change) (*MergePreview, error) {
	repo, err := f.Open(acc.Repo)
	if err != nil {
		return nil, err
	}
	pv := &MergePreview{}
	targetID, err := repo.Resolve(ctx, "refs/heads/"+ch.TargetBranch)
	if err != nil {
		pv.Missing = true
		return pv, nil
	}
	pv.TargetHead = targetID
	head := vcs.RevisionID(ch.HeadRev)
	if anc, _ := repo.IsAncestor(ctx, head, targetID); anc {
		pv.Already = true
		return pv, nil
	}
	if anc, _ := repo.IsAncestor(ctx, targetID, head); anc {
		pv.FastForward = true
		return pv, nil
	}
	if base, err := repo.MergeBase(ctx, targetID, head); err == nil {
		pv.Behind, _ = repo.CountCommits(ctx, base, targetID)
	}
	tree, conflicts, err := repo.MergeTree(ctx, targetID, head)
	if err != nil {
		return nil, err
	}
	pv.Tree, pv.Conflicts = tree, conflicts
	return pv, nil
}

// DefaultMergeMessage is the server-generated merge commit message.
func (f *Forge) DefaultMergeMessage(acc Access, ch *store.Change) string {
	msg := fmt.Sprintf("Merge change #%d: %s\n", ch.Number, ch.Title)
	if ch.Body != "" {
		msg += "\n" + ch.Body + "\n"
	}
	return msg
}

// MergeErr carries a user-facing explanation for a failed merge.
type MergeErr struct {
	Conflicts []string
	Reason    string
}

func (e *MergeErr) Error() string { return e.Reason }

// MergeChange lands a change on its target branch using plumbing only.
func (f *Forge) MergeChange(ctx context.Context, u *store.User, acc Access, ch *store.Change, message string) (vcs.RevisionID, error) {
	if u == nil {
		return "", ErrAuthRequired
	}
	if !acc.CanWrite() {
		return "", ErrForbidden
	}
	if acc.Repo.Archived {
		return "", ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return "", ErrNotLeader
	}
	if ch.State == "merged" || ch.State == "closed" {
		return "", &MergeErr{Reason: "change is " + ch.State}
	}
	repo, err := f.Open(acc.Repo)
	if err != nil {
		return "", err
	}
	target := "refs/heads/" + ch.TargetBranch
	old, err := repo.Resolve(ctx, target)
	if err != nil {
		return "", &MergeErr{Reason: "target branch " + ch.TargetBranch + " is gone; close the change or push a new version to another branch"}
	}
	head := vcs.RevisionID(ch.HeadRev)
	if cur, err := repo.Resolve(ctx, fmt.Sprintf("refs/changes/%d/head", ch.Number)); err != nil || cur != head {
		return "", &MergeErr{Reason: "change is being updated; retry"}
	}
	var newID vcs.RevisionID
	if anc, _ := repo.IsAncestor(ctx, head, old); anc {
		_ = f.Store.MarkChangeMerged(ctx, ch.ID, string(head), u.ID)
		return head, nil
	}
	if anc, _ := repo.IsAncestor(ctx, old, head); anc {
		newID = head
	} else {
		tree, conflicts, err := repo.MergeTree(ctx, old, head)
		if err != nil {
			return "", err
		}
		if len(conflicts) > 0 {
			return "", &MergeErr{Conflicts: conflicts, Reason: "conflicts with " + ch.TargetBranch}
		}
		msg := strings.TrimSpace(message)
		if msg == "" {
			msg = strings.TrimSpace(f.DefaultMergeMessage(acc, ch))
		}
		msg += "\n\nChange: " + f.Config.GeminiURL(changePath(acc.Repo, ch.Number)) + "\n"
		author := vcs.Signature{Name: u.Name, Email: u.Name + "@" + f.Config.Hostname}
		committer := vcs.Signature{Name: "forge", Email: "forge@" + f.Config.Hostname}
		newID, err = repo.CommitTree(ctx, tree, []vcs.RevisionID{old, head}, author, committer, msg)
		if err != nil {
			return "", err
		}
	}
	if err := repo.UpdateRefs(ctx, []vcs.RefUpdate{{Ref: target, New: newID, Old: old}}, fmt.Sprintf("forge: merge change %d", ch.Number)); err != nil {
		if errors.Is(err, vcs.ErrConflict) {
			return "", &MergeErr{Reason: "branch moved, try again"}
		}
		return "", err
	}
	if err := f.Store.MarkChangeMerged(ctx, ch.ID, string(newID), u.ID); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"number": ch.Number, "merged": string(newID), "branch": ch.TargetBranch})
	f.Event(ctx, store.EventChangeMerge, acc.Repo, u, fmt.Sprintf("%s merged change #%d into %s in %s/%s: %s", u.Name, ch.Number, ch.TargetBranch, acc.Repo.Owner, acc.Repo.Name, ch.Title),
		fmt.Sprintf("/~%s/%s/commit/%s", acc.Repo.Owner, acc.Repo.Name, newID), payload)
	if size, err := repo.Size(ctx); err == nil {
		_ = f.Store.RecordPush(ctx, acc.Repo.ID, size)
	}
	if f.OnPush != nil {
		f.OnPush(acc.Repo)
	}
	return newID, nil
}
