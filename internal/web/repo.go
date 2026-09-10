package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
)

// repoCtx is a resolved repository for a request.
type repoCtx struct {
	acc  forge.Access
	repo vcs.Repository
	base string // "/~owner/name"
}

func (h *Handler) userRoutes(req *request, user string, rest []string, trailing bool) {
	if err := forge.ValidUserName(user); err != nil && user != "root" {
		_ = gemini.NotFound(req.w)
		return
	}
	if len(rest) == 0 {
		if !trailing {
			_ = req.w.Header(gemini.StatusRedirectPermanent, "/~"+user+"/")
			return
		}
		h.userPage(req, user)
		return
	}
	if rest[0] == "feed" && len(rest) == 1 {
		h.feedUser(req, user, false)
		return
	}
	if rest[0] == "atom.xml" && len(rest) == 1 {
		h.feedUser(req, user, true)
		return
	}
	name := rest[0]
	rest = rest[1:]
	if strings.HasSuffix(name, ".git") {
		_ = req.w.Header(gemini.StatusRedirectPermanent, "/~"+user+"/"+strings.TrimSuffix(name, ".git")+"/")
		return
	}
	acc, err := h.F.LookupRepo(req.ctx, req.id.User, user, name)
	if err != nil {
		req.fail(err)
		return
	}
	rc := &repoCtx{acc: acc, base: "/~" + user + "/" + name}
	if len(rest) == 0 && !trailing {
		_ = req.w.Header(gemini.StatusRedirectPermanent, rc.base+"/")
		return
	}
	repo, err := h.F.Open(acc.Repo)
	if err != nil {
		h.Log.Error("open repo", "repo", rc.base, "err", err)
		_ = gemini.TemporaryFailure(req.w, "repository unavailable")
		return
	}
	rc.repo = repo
	if len(rest) == 0 {
		h.repoOverview(req, rc)
		return
	}
	switch rest[0] {
	case "tree":
		h.repoTree(req, rc, rest[1:])
	case "raw":
		h.repoRaw(req, rc, rest[1:])
	case "log":
		h.repoLog(req, rc, rest[1:])
	case "commit":
		h.repoCommit(req, rc, rest[1:])
	case "refs":
		h.repoRefs(req, rc)
	case "feed":
		h.feedRepo(req, rc, nil, false)
	case "atom.xml":
		h.feedRepo(req, rc, nil, true)
	case "issues":
		h.issueRoutes(req, rc, rest[1:])
	case "changes":
		h.changeRoutes(req, rc, rest[1:])
	case "releases":
		h.releaseRoutes(req, rc, rest[1:])
	case "settings":
		h.repoSettings(req, rc, rest[1:])
	default:
		_ = gemini.NotFound(req.w)
	}
}

func (h *Handler) userPage(req *request, name string) {
	u, err := h.F.Store.UserByName(req.ctx, name)
	if err != nil {
		req.fail(err)
		return
	}
	title := u.Name
	if u.DisplayName != "" {
		title = u.DisplayName + " (" + u.Name + ")"
	}
	p := req.page(title)
	if u.Bio != "" {
		p.Text(u.Bio)
		p.Blank()
	}
	p.Textf("Member since %s.", date(u.CreatedAt))
	p.Link("/~"+u.Name+"/feed", "activity feed")
	p.Blank()
	repos, err := h.F.Store.ListRepos(req.ctx, store.RepoListOptions{Viewer: req.id.UserID(), Owner: u.ID, Limit: 200})
	if err != nil {
		req.fail(err)
		return
	}
	p.Heading(2, "Repositories")
	if len(repos) == 0 {
		p.Text("None yet.")
	}
	for _, r := range repos {
		label := r.Name
		if r.Description != "" {
			label += " - " + r.Description
		}
		if r.Private {
			label += " (private)"
		}
		if r.Archived {
			label += " (archived)"
		}
		p.Link(fmt.Sprintf("/~%s/%s/", r.Owner, r.Name), label)
	}
	if req.id != nil && req.id.User != nil && req.id.User.ID == u.ID {
		p.Blank()
		p.Link("/new", "create a repository")
		p.Link("/account", "account settings")
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) repoHeader(p *gemini.Page, rc *repoCtx) {
	r := rc.acc.Repo
	if r.Description != "" {
		p.Text(r.Description)
	}
	flags := []string{}
	if r.Private {
		flags = append(flags, "private")
	}
	if r.Archived {
		flags = append(flags, "archived")
	}
	if len(flags) > 0 {
		p.Text("(" + strings.Join(flags, ", ") + ")")
	}
}

func (h *Handler) repoNav(p *gemini.Page, rc *repoCtx, ref string) {
	p.Blank()
	p.Link(rc.base+"/", "overview")
	p.Link(rc.base+"/tree/"+ref+"/", "files")
	p.Link(rc.base+"/log/"+ref, "history")
	p.Link(rc.base+"/refs", "branches and tags")
	p.Link(rc.base+"/issues/", "issues")
	p.Link(rc.base+"/changes/", "changes")
	p.Link(rc.base+"/releases/", "releases")
	p.Link(rc.base+"/feed", "feed")
}

func (h *Handler) repoOverview(req *request, rc *repoCtx) {
	r := rc.acc.Repo
	p := req.page(r.Owner + "/" + r.Name)
	h.repoHeader(p, rc)
	p.Blank()
	p.Heading(2, "Clone")
	p.Pre("clone", "git clone "+h.F.Config.CloneURL(r.Owner, r.Name))
	empty, err := rc.repo.Empty(req.ctx)
	if err != nil {
		req.fail(err)
		return
	}
	if empty {
		p.Blank()
		p.Text("This repository is empty. Push to create the first branch:")
		p.Pre("push", fmt.Sprintf("git remote add origin %s\ngit push -u origin %s", h.F.Config.CloneURL(r.Owner, r.Name), r.DefaultBranch))
		p.Blank()
		p.Link(rc.base+"/issues/", "issues")
		if rc.acc.CanAdmin() {
			p.Link(rc.base+"/settings", "settings")
		}
		h.footer(p, req)
		req.send(p)
		return
	}
	ref := r.DefaultBranch
	head, err := rc.repo.Resolve(req.ctx, ref)
	if err != nil {
		// Default branch missing: fall back to any branch.
		refs, _ := rc.repo.Refs(req.ctx)
		for _, x := range refs {
			if x.Kind == vcs.RefBranch {
				ref, head = x.Name, x.Target
				break
			}
		}
	}
	h.repoNav(p, rc, ref)
	if rc.acc.CanAdmin() {
		p.Link(rc.base+"/settings", "settings")
	}
	if head != "" {
		if revs, err := rc.repo.Log(req.ctx, head, vcs.LogOptions{Limit: 5}); err == nil && len(revs) > 0 {
			p.Blank()
			p.Heading(2, "Latest commits on "+ref)
			for _, rev := range revs {
				p.Link(rc.base+"/commit/"+string(rev.ID), fmt.Sprintf("%s %s %s (%s)", date(rev.Author.When), short(string(rev.ID)), rev.Subject, rev.Author.Name))
			}
		}
		h.renderReadme(req, p, rc, head, ref)
	}
	h.footer(p, req)
	req.send(p)
}

var readmeNames = []string{"README.gmi", "README.gemini", "readme.gmi", "README.md", "README", "README.txt", "readme.md", "Readme.md"}

func (h *Handler) renderReadme(req *request, p *gemini.Page, rc *repoCtx, head vcs.RevisionID, ref string) {
	entries, err := rc.repo.Tree(req.ctx, head, "")
	if err != nil {
		return
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.Kind == vcs.EntryFile || e.Kind == vcs.EntryExecutable {
			present[e.Name] = true
		}
	}
	for _, name := range readmeNames {
		if !present[name] {
			continue
		}
		blob, err := rc.repo.Blob(req.ctx, head, name)
		if err != nil {
			return
		}
		data, _ := io.ReadAll(io.LimitReader(blob.Reader, h.F.Config.Limits.MaxRenderBytes))
		_ = blob.Close()
		if !utf8.Valid(data) {
			return
		}
		p.Blank()
		p.Heading(2, name)
		text := string(data)
		switch {
		case strings.HasSuffix(name, ".gmi") || strings.HasSuffix(name, ".gemini"):
			p.Raw(rebaseGemtextLinks(text, rc.base+"/tree/"+ref+"/"))
		case strings.HasSuffix(strings.ToLower(name), ".md"):
			p.Raw(MarkdownToGemtext(text, rc.base+"/tree/"+ref+"/"))
		default:
			p.Pre(name, text)
		}
		if blob.Size > h.F.Config.Limits.MaxRenderBytes {
			p.Link(rc.base+"/raw/"+ref+"/"+name, "(truncated; full file)")
		}
		return
	}
}

// splitRefPath resolves "<ref>/<path>" where ref may itself contain slashes.
func (h *Handler) splitRefPath(req *request, rc *repoCtx, segs []string) (ref string, id vcs.RevisionID, rest []string, ok bool) {
	if len(segs) == 0 {
		return "", "", nil, false
	}
	refs, err := rc.repo.Refs(req.ctx)
	if err != nil {
		return "", "", nil, false
	}
	known := map[string]vcs.RevisionID{}
	for _, r := range refs {
		known[r.Name] = r.Target
	}
	for i := len(segs); i >= 1; i-- {
		cand := strings.Join(segs[:i], "/")
		if t, ok := known[cand]; ok {
			return cand, t, segs[i:], true
		}
	}
	// A commit id or abbreviation.
	if id, err := rc.repo.Resolve(req.ctx, segs[0]); err == nil {
		return segs[0], id, segs[1:], true
	}
	return "", "", nil, false
}

func (h *Handler) repoTree(req *request, rc *repoCtx, segs []string) {
	r := rc.acc.Repo
	if len(segs) == 0 {
		_ = gemini.Redirect(req.w, rc.base+"/tree/"+r.DefaultBranch+"/")
		return
	}
	ref, id, rest, ok := h.splitRefPath(req, rc, segs)
	if !ok {
		_ = gemini.NotFound(req.w)
		return
	}
	// A trailing empty segment means the client asked for a directory.
	trailing := strings.HasSuffix(req.Path(), "/")
	if len(rest) > 0 && rest[len(rest)-1] == "" {
		rest = rest[:len(rest)-1]
	}
	subpath := strings.Join(rest, "/")
	entries, err := rc.repo.Tree(req.ctx, id, subpath)
	if errors.Is(err, vcs.ErrNotDir) {
		h.repoBlob(req, rc, ref, id, subpath)
		return
	}
	if err != nil {
		if errors.Is(err, vcs.ErrNotFound) || errors.Is(err, vcs.ErrBadPath) {
			_ = gemini.NotFound(req.w)
			return
		}
		req.fail(err)
		return
	}
	if !trailing {
		_ = req.w.Header(gemini.StatusRedirectPermanent, req.Path()+"/")
		return
	}
	title := r.Owner + "/" + r.Name
	if subpath != "" {
		title += "/" + subpath
	}
	p := req.page(title)
	p.Textf("At %s", ref)
	p.Blank()
	if subpath != "" {
		parent := path.Dir(subpath)
		if parent == "." {
			parent = ""
		} else {
			parent += "/"
		}
		p.Link(rc.base+"/tree/"+ref+"/"+parent, "..")
	} else {
		p.Link(rc.base+"/", "repository overview")
	}
	for _, e := range entries {
		href := rc.base + "/tree/" + ref + "/" + joinPath(subpath, e.Name)
		switch e.Kind {
		case vcs.EntryDir:
			p.Link(href+"/", e.Name+"/")
		case vcs.EntrySubmodule:
			p.Item(e.Name + " (submodule " + short(e.ID) + ")")
		case vcs.EntrySymlink:
			p.Link(href, e.Name+" (symlink)")
		case vcs.EntryExecutable:
			p.Link(href, fmt.Sprintf("%s* (%s)", e.Name, humanBytes(e.Size)))
		default:
			p.Link(href, fmt.Sprintf("%s (%s)", e.Name, humanBytes(e.Size)))
		}
	}
	p.Blank()
	if subpath != "" {
		p.Link(rc.base+"/log/"+ref+"/"+subpath, "history of this directory")
	}
	h.footer(p, req)
	req.send(p)
}

func joinPath(dir, name string) string {
	name = url.PathEscape(name)
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (h *Handler) repoBlob(req *request, rc *repoCtx, ref string, id vcs.RevisionID, subpath string) {
	blob, err := rc.repo.Blob(req.ctx, id, subpath)
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	defer blob.Close()
	limit := h.F.Config.Limits.MaxRenderBytes
	data, err := io.ReadAll(io.LimitReader(blob.Reader, limit))
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page(rc.acc.Repo.Owner + "/" + rc.acc.Repo.Name + "/" + subpath)
	p.Textf("At %s, %s", ref, humanBytes(blob.Size))
	p.Link(rc.base+"/tree/"+ref+"/"+parentDir(subpath), "directory")
	p.Link(rc.base+"/raw/"+ref+"/"+subpath, "raw")
	p.Link(rc.base+"/log/"+ref+"/"+subpath, "history")
	p.Blank()
	if isBinary(data) {
		p.Text("Binary file; use the raw link.")
	} else {
		p.Pre(path.Base(subpath), string(data))
		if blob.Size > limit {
			p.Text("(truncated; use the raw link for the full file)")
		}
	}
	h.footer(p, req)
	req.send(p)
}

func parentDir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d + "/"
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	for _, b := range data[:min(len(data), 8000)] {
		if b == 0 {
			return true
		}
	}
	return false
}

func (h *Handler) repoRaw(req *request, rc *repoCtx, segs []string) {
	ref, id, rest, ok := h.splitRefPath(req, rc, segs)
	_ = ref
	if !ok || len(rest) == 0 {
		_ = gemini.NotFound(req.w)
		return
	}
	subpath := strings.Join(rest, "/")
	blob, err := rc.repo.Blob(req.ctx, id, subpath)
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	defer blob.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(blob.Reader, head)
	head = head[:n]
	ctype := mime.TypeByExtension(path.Ext(subpath))
	if ctype == "" || strings.HasPrefix(ctype, "text/") {
		if isBinary(head) {
			ctype = "application/octet-stream"
		} else if ctype == "" {
			ctype = "text/plain; charset=utf-8"
		} else if !strings.Contains(ctype, "charset") {
			ctype += "; charset=utf-8"
		}
	}
	// Repository gemtext is never served as text/gemini: user-authored link
	// lines would render unmarked (threat model T-24). Rendered views mark
	// links; raw is plain text.
	if strings.HasSuffix(subpath, ".gmi") || strings.HasSuffix(subpath, ".gemini") {
		ctype = "text/plain; charset=utf-8"
	}
	_ = req.w.Header(gemini.StatusSuccess, ctype)
	_, _ = req.w.Write(head)
	_, _ = io.Copy(req.w, blob.Reader)
}

func (h *Handler) repoLog(req *request, rc *repoCtx, segs []string) {
	r := rc.acc.Repo
	if len(segs) == 0 {
		_ = gemini.Redirect(req.w, rc.base+"/log/"+r.DefaultBranch)
		return
	}
	ref, id, rest, ok := h.splitRefPath(req, rc, segs)
	if !ok {
		_ = gemini.NotFound(req.w)
		return
	}
	subpath := strings.Join(rest, "/")
	skip, _ := strconv.Atoi(req.Query())
	if skip < 0 {
		skip = 0
	}
	const pageSize = 50
	revs, err := rc.repo.Log(req.ctx, id, vcs.LogOptions{Path: subpath, Skip: skip, Limit: pageSize + 1})
	if err != nil {
		req.fail(err)
		return
	}
	title := "History of " + r.Owner + "/" + r.Name + " at " + ref
	if subpath != "" {
		title += " for " + subpath
	}
	p := req.page(title)
	p.Link(rc.base+"/", "repository overview")
	p.Blank()
	more := len(revs) > pageSize
	if more {
		revs = revs[:pageSize]
	}
	for _, rev := range revs {
		p.Link(rc.base+"/commit/"+string(rev.ID), fmt.Sprintf("%s %s %s (%s)", date(rev.Author.When), short(string(rev.ID)), rev.Subject, rev.Author.Name))
	}
	if more {
		p.Blank()
		p.Link(fmt.Sprintf("%s?%d", req.Path(), skip+pageSize), "older")
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) repoCommit(req *request, rc *repoCtx, segs []string) {
	if len(segs) != 1 {
		_ = gemini.NotFound(req.w)
		return
	}
	id, err := rc.repo.Resolve(req.ctx, segs[0])
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	rev, err := rc.repo.Revision(req.ctx, id)
	if err != nil {
		req.fail(err)
		return
	}
	r := rc.acc.Repo
	p := req.page(rev.Subject)
	p.Textf("Commit %s in %s/%s", rev.ID, r.Owner, r.Name)
	p.Textf("Author: %s <%s> %s", rev.Author.Name, rev.Author.Email, when(rev.Author.When))
	if rev.Committer.Email != rev.Author.Email || rev.Committer.Name != rev.Author.Name {
		p.Textf("Committer: %s <%s> %s", rev.Committer.Name, rev.Committer.Email, when(rev.Committer.When))
	}
	for _, par := range rev.Parents {
		p.Link(rc.base+"/commit/"+string(par), "parent "+short(string(par)))
	}
	p.Link(rc.base+"/tree/"+string(rev.ID)+"/", "browse files at this commit")
	if rev.Body != "" {
		p.Blank()
		p.Text(rev.Body)
	}
	d, err := rc.repo.Diff(req.ctx, id, h.F.Config.Limits.MaxDiffBytes)
	if err != nil {
		req.fail(err)
		return
	}
	p.Blank()
	p.Heading(2, "Changes")
	for _, st := range d.Stats {
		name := st.Path
		if st.OldPath != "" {
			name = st.OldPath + " -> " + st.Path
		}
		if st.Binary {
			p.Item(name + " (binary)")
		} else {
			p.Item(fmt.Sprintf("%s +%d -%d", name, st.Added, st.Deleted))
		}
	}
	if len(d.Stats) == 0 {
		p.Text("No file changes (merge or empty commit).")
	}
	if d.Patch != "" {
		p.Blank()
		p.Pre("diff", d.Patch)
		if d.Truncated {
			p.Text("(diff truncated)")
		}
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) repoRefs(req *request, rc *repoCtx) {
	refs, err := rc.repo.Refs(req.ctx)
	if err != nil {
		req.fail(err)
		return
	}
	r := rc.acc.Repo
	p := req.page("Branches and tags of " + r.Owner + "/" + r.Name)
	p.Link(rc.base+"/", "repository overview")
	p.Blank()
	p.Heading(2, "Branches")
	for _, x := range refs {
		if x.Kind == vcs.RefBranch {
			label := x.Name
			if x.Name == r.DefaultBranch {
				label += " (default)"
			}
			p.Link(rc.base+"/log/"+x.Name, fmt.Sprintf("%s %s %s", date(x.When), label, short(string(x.Target))))
		}
	}
	p.Blank()
	p.Heading(2, "Tags")
	n := 0
	for _, x := range refs {
		if x.Kind == vcs.RefTag {
			n++
			label := fmt.Sprintf("%s %s %s", date(x.When), x.Name, short(string(x.Target)))
			if x.Message != "" {
				label += " - " + x.Message
			}
			p.Link(rc.base+"/commit/"+string(x.Target), label)
		}
	}
	if n == 0 {
		p.Text("No tags.")
	}
	h.footer(p, req)
	req.send(p)
}
