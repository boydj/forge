package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"as215520.net/forge/internal/hooks"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
)

const zeroID = "0000000000000000000000000000000000000000"

// HandleHook implements hooks.Handler: pre-receive authorisation and
// post-receive bookkeeping for pushes.
func (f *Forge) HandleHook(ctx context.Context, req *hooks.Request) *hooks.Response {
	deny := func(msg string) *hooks.Response {
		return &hooks.Response{OK: false, Messages: []string{"forge: " + msg}}
	}
	owner, name, ok := strings.Cut(req.Repo, "/")
	if !ok {
		return deny("malformed repository name")
	}
	r, err := f.Store.RepoByPath(ctx, owner, name)
	if err != nil {
		return deny("repository not found")
	}
	u, err := f.Store.UserByID(ctx, req.AccountID)
	if err != nil || u.Disabled || u.Name != req.Account {
		return deny("account not permitted")
	}
	role, err := f.Permission(ctx, u, r)
	if err != nil {
		return deny("permission check failed")
	}
	switch req.Hook {
	case "pre-receive":
		return f.preReceive(ctx, req, r, u, role, deny)
	case "post-receive":
		f.postReceive(ctx, req, r, u)
		return &hooks.Response{OK: true}
	case "proc-receive":
		return f.procReceive(ctx, req, r, u, role)
	default:
		return deny("unknown hook")
	}
}

func (f *Forge) preReceive(ctx context.Context, req *hooks.Request, r *store.Repo, u *store.User, role store.Role, deny func(string) *hooks.Response) *hooks.Response {
	writer := role == store.RoleWrite || role == store.RoleAdmin
	if !writer && role != store.RoleRead {
		return deny("you do not have access to " + req.Repo)
	}
	if r.Archived {
		return deny("repository is archived; unarchive it in settings to push")
	}
	if !f.IsLeader(r) {
		return deny("this node does not accept pushes for " + req.Repo + " (leader: " + r.LeaderNode + ")")
	}
	if err := f.CheckDisk(req.PushedBytes); err != nil {
		return deny("server is low on disk space")
	}
	if !u.Admin && r.SizeBytes+req.PushedBytes > f.Config.Limits.MaxRepoBytes {
		return deny(fmt.Sprintf("repository size limit (%d bytes) would be exceeded", f.Config.Limits.MaxRepoBytes))
	}
	if !u.Admin {
		_, total, err := f.Store.CountReposByOwner(ctx, r.OwnerID)
		if err == nil && total+req.PushedBytes > f.Config.Limits.MaxUserBytes {
			return deny("owner's storage quota would be exceeded")
		}
	}
	if !writer && req.PushedBytes > f.Config.Limits.MaxChangeBytes {
		return deny(fmt.Sprintf("a proposed change may not exceed %d bytes per push", f.Config.Limits.MaxChangeBytes))
	}
	changeCmds := 0
	for _, up := range req.Updates {
		switch {
		case strings.HasPrefix(up.Ref, "refs/for/"), isChangeRef(up.Ref):
			changeCmds++
			if up.New == zeroID {
				return deny("cannot delete " + up.Ref)
			}
		case strings.HasPrefix(up.Ref, "refs/heads/"), strings.HasPrefix(up.Ref, "refs/tags/"), strings.HasPrefix(up.Ref, "refs/notes/"):
			if !writer {
				return deny("you can only propose changes here: git push origin HEAD:refs/for/" + r.DefaultBranch)
			}
		default:
			return deny("ref " + up.Ref + " is not allowed; push branches, tags, notes, refs/for/<branch> or refs/changes/<n>")
		}
		if up.New == zeroID && up.Ref == "refs/heads/"+r.DefaultBranch {
			return deny("refusing to delete the default branch " + r.DefaultBranch)
		}
		if len(up.Ref) > 512 || strings.ContainsAny(up.Ref, " \t\n") {
			return deny("invalid ref name")
		}
	}
	if changeCmds > 1 {
		return deny("push one change (refs/for/<branch> or refs/changes/<n>) at a time")
	}
	if changeCmds == 1 && !writer {
		n, err := f.Store.CountOpenChangesByAuthor(ctx, r.ID, u.ID)
		if err == nil && n >= f.Config.Limits.MaxOpenChangesPerUser {
			return deny(fmt.Sprintf("you already have %d open changes in %s; close or finish some first", n, req.Repo))
		}
	}
	return &hooks.Response{OK: true}
}

// isChangeRef matches refs/changes/<n> exactly (no sub-path).
func isChangeRef(ref string) bool {
	rest, ok := strings.CutPrefix(ref, "refs/changes/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (f *Forge) postReceive(ctx context.Context, req *hooks.Request, r *store.Repo, u *store.User) {
	repo, err := f.Open(r)
	if err != nil {
		f.Log.Error("post-receive open", "repo", req.Repo, "err", err)
		return
	}
	base := "/~" + r.Owner + "/" + r.Name
	for _, up := range req.Updates {
		var subject, path, kind string
		payload := map[string]any{"ref": up.Ref, "old": up.Old, "new": up.New}
		switch {
		case strings.HasPrefix(up.Ref, "refs/heads/"):
			branch := strings.TrimPrefix(up.Ref, "refs/heads/")
			kind = store.EventPush
			switch {
			case up.New == zeroID:
				subject = fmt.Sprintf("%s deleted branch %s in %s/%s", u.Name, branch, r.Owner, r.Name)
				path = base + "/refs"
			case up.Old == zeroID:
				n := f.countCommits(ctx, repo, "", up.New)
				subject = fmt.Sprintf("%s created branch %s in %s/%s (%s)", u.Name, branch, r.Owner, r.Name, plural(n, "commit"))
				path = base + "/log/" + branch
				payload["commits"] = n
			default:
				n := f.countCommits(ctx, repo, up.Old, up.New)
				title := ""
				if rev, err := repo.Revision(ctx, vcs.RevisionID(up.New)); err == nil {
					title = ": " + rev.Subject
				}
				subject = fmt.Sprintf("%s pushed %s to %s in %s/%s%s", u.Name, plural(n, "commit"), branch, r.Owner, r.Name, title)
				path = base + "/commit/" + up.New
				payload["commits"] = n
			}
		case strings.HasPrefix(up.Ref, "refs/tags/"):
			tag := strings.TrimPrefix(up.Ref, "refs/tags/")
			kind = store.EventPush
			if up.New == zeroID {
				subject = fmt.Sprintf("%s deleted tag %s in %s/%s", u.Name, tag, r.Owner, r.Name)
				path = base + "/refs"
			} else {
				subject = fmt.Sprintf("%s tagged %s in %s/%s", u.Name, tag, r.Owner, r.Name)
				path = base + "/refs"
			}
		default:
			// refs/changes/* updates were recorded by proc-receive.
			continue
		}
		b, _ := json.Marshal(payload)
		f.Event(ctx, kind, r, u, subject, path, b)
	}
	size, err := repo.Size(ctx)
	if err != nil {
		size = r.SizeBytes
	}
	if err := f.Store.RecordPush(ctx, r.ID, size); err != nil {
		f.Log.Error("record push", "repo", req.Repo, "err", err)
	}
	if f.OnPush != nil {
		f.OnPush(r)
	}
}

func (f *Forge) countCommits(ctx context.Context, repo vcs.Repository, old, new string) int {
	n := 0
	limit := 1000
	for skip := 0; ; skip += limit {
		revs, err := repo.Log(ctx, vcs.RevisionID(new), vcs.LogOptions{Skip: skip, Limit: limit})
		if err != nil {
			return n
		}
		for _, rev := range revs {
			if old != "" && string(rev.ID) == old {
				return n
			}
			n++
		}
		if len(revs) < limit || old == "" {
			return n
		}
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
