package web

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
	"as215520.net/forge/pkg/gemini"
)

// Change routes (ADR 0012):
//
//	/~o/r/changes/                       list (?open default, ?merged, ?closed, ?all)
//	/~o/r/changes/feed
//	/~o/r/changes/<n>                    change page
//	/~o/r/changes/<n>/diff[/<path>]      current version diff
//	/~o/r/changes/<n>/v<k>/diff[/<path>]
//	/~o/r/changes/<n>/commits, /v<k>/commits
//	/~o/r/changes/<n>/interdiff/<j>/<k>
//	/~o/r/changes/<n>/patch, /v<k>/patch
//	/~o/r/changes/<n>/feed
//	Titan: <n>/comment, <n>/review, <n>/edit, <n>/close, <n>/reopen, <n>/merge
func (h *Handler) changeRoutes(req *request, rc *repoCtx, rest []string) {
	trailing := strings.HasSuffix(req.Path(), "/")
	if len(rest) == 0 {
		if !trailing {
			_ = req.w.Header(gemini.StatusRedirectPermanent, rc.base+"/changes/")
			return
		}
		h.changeList(req, rc)
		return
	}
	changeKinds := []string{store.EventChangeOpen, store.EventChangeUpdate, store.EventChangeMerge, store.EventChangeClose, store.EventReview}
	switch rest[0] {
	case "feed":
		h.feedRepo(req, rc, changeKinds, false)
		return
	case "atom.xml":
		h.feedRepo(req, rc, changeKinds, true)
		return
	}
	n, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || n <= 0 {
		_ = gemini.NotFound(req.w)
		return
	}
	ch, err := h.F.LookupChange(req.ctx, rc.acc, n)
	if err != nil {
		req.fail(err)
		return
	}
	if len(rest) == 1 {
		h.changePage(req, rc, ch)
		return
	}
	versions, err := h.F.Store.ListChangeVersions(req.ctx, ch.ID)
	if err != nil {
		req.fail(err)
		return
	}
	cur := versions[len(versions)-1]
	sub := rest[1:]
	// Optional version selector v<k>.
	if v, ok := strings.CutPrefix(sub[0], "v"); ok && len(sub) > 1 {
		k, err := strconv.Atoi(v)
		if err != nil || k < 1 || k > len(versions) {
			_ = gemini.NotFound(req.w)
			return
		}
		cur = versions[k-1]
		sub = sub[1:]
	}
	switch sub[0] {
	case "diff":
		h.changeDiff(req, rc, ch, cur, strings.Join(sub[1:], "/"))
	case "commits":
		h.changeCommits(req, rc, ch, cur)
	case "patch":
		h.changePatch(req, rc, ch, cur)
	case "interdiff":
		if len(sub) != 3 {
			_ = gemini.NotFound(req.w)
			return
		}
		j, err1 := strconv.Atoi(sub[1])
		k, err2 := strconv.Atoi(sub[2])
		if err1 != nil || err2 != nil || j < 1 || k < 1 || j > len(versions) || k > len(versions) || j == k {
			_ = gemini.NotFound(req.w)
			return
		}
		h.changeInterdiff(req, rc, ch, versions[j-1], versions[k-1])
	case "feed", "atom.xml":
		h.changeFeed(req, rc, ch, sub[0] == "atom.xml")
	case "comment", "review", "edit", "close", "reopen", "merge":
		p := req.page(fmt.Sprintf("Change #%d: %s", ch.Number, sub[0]))
		p.Textf("This action takes a Titan upload to %s", h.F.Config.TitanURL(req.Path()))
		switch sub[0] {
		case "review":
			p.Text("First line: approve, request-changes or comment. Then your review. Anchor a section to a file with a line \"@ path:line\" and quote diff lines with \"> \".")
		case "merge":
			p.Text("Body: merge commit message (empty for the default).")
		case "edit":
			p.Text("First line: title. Then the description.")
		case "close", "reopen":
			p.Text("Body: optional comment (size=0 is fine).")
		}
		p.Link(changeHref(rc, ch), "back to the change")
		req.send(p)
	default:
		_ = gemini.NotFound(req.w)
	}
}

func changeHref(rc *repoCtx, ch *store.Change) string {
	return fmt.Sprintf("%s/changes/%d", rc.base, ch.Number)
}

func stateLabel(s string) string {
	return strings.ReplaceAll(s, "-", " ")
}

func (h *Handler) changeList(req *request, rc *repoCtx) {
	r := rc.acc.Repo
	filter := "open"
	switch req.Query() {
	case "merged", "closed":
		filter = req.Query()
	case "all":
		filter = ""
	}
	chs, err := h.F.Store.ListChanges(req.ctx, r.ID, filter, 100)
	if err != nil {
		req.fail(err)
		return
	}
	open, merged, closed, _ := h.F.Store.CountChanges(req.ctx, r.ID)
	p := req.page("Changes of " + r.Owner + "/" + r.Name)
	p.Link(rc.base+"/", "repository overview")
	p.Link(rc.base+"/changes/?open", fmt.Sprintf("open (%d)", open))
	p.Link(rc.base+"/changes/?merged", fmt.Sprintf("merged (%d)", merged))
	p.Link(rc.base+"/changes/?closed", fmt.Sprintf("closed (%d)", closed))
	p.Link(rc.base+"/changes/feed", "feed")
	p.Blank()
	for _, ch := range chs {
		p.Link(changeHref(rc, ch), fmt.Sprintf("%s #%d %s (%s, v%d, %s, -> %s)", date(ch.UpdatedAt), ch.Number, ch.Title, ch.Author, ch.Version, stateLabel(ch.State), ch.TargetBranch))
	}
	if len(chs) == 0 {
		p.Text("None.")
	}
	p.Blank()
	p.Heading(2, "Propose a change")
	p.Text("Push your commits to the branch you are targeting through the refs/for namespace; the forge creates the change and prints its address:")
	p.Pre("propose", fmt.Sprintf("git push %s HEAD:refs/for/%s", h.F.Config.CloneURL(r.Owner, r.Name), r.DefaultBranch))
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) changePage(req *request, rc *repoCtx, ch *store.Change) {
	versions, err := h.F.Store.ListChangeVersions(req.ctx, ch.ID)
	if err != nil || len(versions) == 0 {
		req.fail(err)
		return
	}
	cur := versions[len(versions)-1]
	reviews, _ := h.F.Store.ListReviews(req.ctx, ch.ID)
	comments, _ := h.F.Store.ListComments(req.ctx, "change", ch.ID)
	p := req.page(fmt.Sprintf("#%d %s", ch.Number, ch.Title))
	p.Textf("%s -> %s, v%d, %s. Opened %s, updated %s.", ch.Author, ch.TargetBranch, ch.Version, stateLabel(ch.State), date(ch.CreatedAt), date(ch.UpdatedAt))
	if ch.Topic != "" {
		p.Textf("Topic: %s", ch.Topic)
	}
	if ch.State == "merged" {
		p.Link(rc.base+"/commit/"+ch.MergedRev, "merged as "+short(ch.MergedRev))
	}
	if ch.Body != "" {
		p.Blank()
		p.Raw(userGemtext(ch.Body))
	}
	p.Blank()
	p.Link(changeHref(rc, ch)+"/diff", fmt.Sprintf("diff of v%d", cur.Number))
	p.Link(changeHref(rc, ch)+"/commits", "commits")
	p.Link(changeHref(rc, ch)+"/patch", "patch (mbox)")
	p.Link(changeHref(rc, ch)+"/feed", "feed for this change")
	p.Pre("fetch", fmt.Sprintf("git fetch %s refs/changes/%d/head && git checkout FETCH_HEAD", h.F.Config.CloneURL(rc.acc.Repo.Owner, rc.acc.Repo.Name), ch.Number))
	p.Blank()
	p.Heading(2, "Versions")
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		label := fmt.Sprintf("%s v%d %s %s (base %s)", date(v.CreatedAt), v.Number, short(v.HeadRev), plural(v.Commits, "commit"), short(v.BaseRev))
		if v.Number == cur.Number {
			label += " current"
		}
		p.Link(fmt.Sprintf("%s/v%d/diff", changeHref(rc, ch), v.Number), label)
		if i > 0 {
			p.Link(fmt.Sprintf("%s/interdiff/%d/%d", changeHref(rc, ch), versions[i-1].Number, v.Number), fmt.Sprintf("v%d -> v%d interdiff", versions[i-1].Number, v.Number))
		}
	}
	// Files of the current version.
	if d, err := rc.repo.DiffRange(req.ctx, vcs.RevisionID(cur.BaseRev), vcs.RevisionID(cur.HeadRev), 1); err == nil {
		p.Blank()
		p.Heading(2, fmt.Sprintf("Files (v%d)", cur.Number))
		for _, st := range d.Stats {
			name := st.Path
			if st.OldPath != "" {
				name = st.OldPath + " -> " + st.Path
			}
			if st.Binary {
				p.Link(fmt.Sprintf("%s/v%d/diff/%s", changeHref(rc, ch), cur.Number, st.Path), name+" (binary)")
			} else {
				p.Link(fmt.Sprintf("%s/v%d/diff/%s", changeHref(rc, ch), cur.Number, st.Path), fmt.Sprintf("%s +%d -%d", name, st.Added, st.Deleted))
			}
		}
	}
	p.Blank()
	p.Heading(2, "Reviews")
	if len(reviews) == 0 {
		p.Text("None yet.")
	}
	for _, rv := range reviews {
		verb := map[string]string{"approve": "approved", "request-changes": "requested changes on", "comment": "commented on"}[rv.Verdict]
		label := fmt.Sprintf("%s %s v%d - %s", rv.Reviewer, verb, rv.Version, date(rv.CreatedAt))
		if rv.Version < cur.Number {
			label += " (stale)"
		}
		if !rv.Counts && rv.Verdict != "comment" {
			label += " (does not count)"
		}
		p.Heading(3, label)
		p.Raw(h.renderAnchored(rc, ch, rv.Body, rv.Version))
	}
	p.Blank()
	p.Heading(2, "Comments")
	if len(comments) == 0 {
		p.Text("None yet.")
	}
	for _, c := range comments {
		ver, _ := h.F.Store.CommentVersion(req.ctx, c.ID)
		p.Heading(3, fmt.Sprintf("%s - %s on v%d", c.Author, when(c.CreatedAt), ver))
		p.Raw(h.renderAnchored(rc, ch, c.Body, ver))
	}
	if rc.acc.CanWrite() && ch.State != "merged" && ch.State != "closed" {
		p.Blank()
		p.Heading(2, "Merge")
		pv, err := h.F.PreviewMerge(req.ctx, rc.acc, ch)
		switch {
		case err != nil:
			p.Text("Merge preview unavailable.")
		case pv.Missing:
			p.Textf("Target branch %s no longer exists.", ch.TargetBranch)
		case pv.Already:
			p.Textf("Already contained in %s.", ch.TargetBranch)
		case pv.FastForward:
			p.Textf("Fast-forward: %s will advance to %s.", ch.TargetBranch, short(ch.HeadRev))
		case len(pv.Conflicts) > 0:
			p.Textf("Conflicts in: %s - the author must rebase onto %s and push a new version.", strings.Join(pv.Conflicts, ", "), ch.TargetBranch)
		default:
			p.Textf("Merge commit will be created (%s has %d new commits).", ch.TargetBranch, pv.Behind)
			p.Pre("merge message", h.F.DefaultMergeMessage(rc.acc, ch))
		}
	}
	p.Blank()
	p.Heading(2, "Actions")
	if req.id.User != nil && !rc.acc.Repo.Archived {
		p.Textf("Comment: Titan upload to %s", h.F.Config.TitanURL(changeHref(rc, ch)+"/comment"))
		p.Textf("Review: Titan upload to %s (first line approve, request-changes or comment)", h.F.Config.TitanURL(changeHref(rc, ch)+"/review"))
		if canManage := rc.acc.CanWrite() || ch.AuthorID == req.id.UserID(); canManage {
			p.Textf("Edit title/description: Titan upload to %s", h.F.Config.TitanURL(changeHref(rc, ch)+"/edit"))
			if ch.State == "closed" {
				p.Textf("Reopen: Titan upload (may be empty) to %s", h.F.Config.TitanURL(changeHref(rc, ch)+"/reopen"))
			} else if ch.State != "merged" {
				p.Textf("Close: Titan upload (may be empty) to %s", h.F.Config.TitanURL(changeHref(rc, ch)+"/close"))
			}
		}
		if rc.acc.CanWrite() && ch.State != "merged" && ch.State != "closed" {
			p.Textf("Merge: Titan upload (merge message, may be empty) to %s", h.F.Config.TitanURL(changeHref(rc, ch)+"/merge"))
		}
		p.Pre("new version", fmt.Sprintf("git push %s HEAD:refs/changes/%d", h.F.Config.CloneURL(rc.acc.Repo.Owner, rc.acc.Repo.Name), ch.Number))
	}
	p.Link(rc.base+"/changes/", "all changes")
	h.footer(p, req)
	req.send(p)
}

var anchorRe = regexp.MustCompile(`^@ (\S+?)(?::(\d+))?(?: v(\d+))?\s*$`)

// safeAnchorPath accepts only plain relative repository paths in anchors.
func safeAnchorPath(p string) bool {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "?#\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

// renderAnchored renders review/comment text with "@ path[:line] [vK]"
// anchor lines turned into diff links (ADR 0012 section 7).
func (h *Handler) renderAnchored(rc *repoCtx, ch *store.Change, body string, defaultVersion int) string {
	var out strings.Builder
	var plain []string
	flush := func() {
		if len(plain) > 0 {
			out.WriteString(userGemtext(strings.Join(plain, "\n")))
			plain = nil
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if m := anchorRe.FindStringSubmatch(l); m != nil {
			flush()
			path, line, ver := m[1], m[2], defaultVersion
			if m[3] != "" {
				ver, _ = strconv.Atoi(m[3])
			}
			if ver <= 0 || ver > ch.Version {
				ver = ch.Version
			}
			if !safeAnchorPath(path) {
				plain = append(plain, l)
				continue
			}
			label := path
			if line != "" {
				label += ":" + line
			}
			out.WriteString(fmt.Sprintf("=> %s/v%d/diff/%s %s (v%d) [user link]\n", changeHref(rc, ch), ver, url.PathEscape(path), label, ver))
			continue
		}
		plain = append(plain, l)
	}
	flush()
	return out.String()
}

func (h *Handler) changeDiff(req *request, rc *repoCtx, ch *store.Change, v *store.ChangeVersion, path string) {
	base, head := vcs.RevisionID(v.BaseRev), vcs.RevisionID(v.HeadRev)
	var d *vcs.Diff
	var err error
	if path != "" {
		d, err = rc.repo.DiffPath(req.ctx, base, head, path, h.F.Config.Limits.MaxDiffBytes)
	} else {
		d, err = rc.repo.DiffRange(req.ctx, base, head, h.F.Config.Limits.MaxDiffBytes)
	}
	if err != nil {
		if errors.Is(err, vcs.ErrBadPath) || errors.Is(err, vcs.ErrNotFound) {
			_ = gemini.NotFound(req.w)
			return
		}
		req.fail(err)
		return
	}
	title := fmt.Sprintf("#%d v%d diff", ch.Number, v.Number)
	if path != "" {
		title += " of " + path
	}
	p := req.page(title)
	p.Textf("%s: %s..%s", ch.Title, short(v.BaseRev), short(v.HeadRev))
	p.Link(changeHref(rc, ch), "back to the change")
	p.Blank()
	if path == "" {
		for _, st := range d.Stats {
			if st.Binary {
				p.Link(fmt.Sprintf("%s/v%d/diff/%s", changeHref(rc, ch), v.Number, st.Path), st.Path+" (binary)")
			} else {
				p.Link(fmt.Sprintf("%s/v%d/diff/%s", changeHref(rc, ch), v.Number, st.Path), fmt.Sprintf("%s +%d -%d", st.Path, st.Added, st.Deleted))
			}
		}
		p.Blank()
	}
	if d.Patch == "" {
		p.Text("No textual changes.")
	} else {
		p.Pre("diff", d.Patch)
		if d.Truncated {
			p.Text("(diff truncated; use the per-file links or the patch download)")
		}
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) changeCommits(req *request, rc *repoCtx, ch *store.Change, v *store.ChangeVersion) {
	revs, err := rc.repo.ListCommits(req.ctx, vcs.RevisionID(v.BaseRev), vcs.RevisionID(v.HeadRev), 500)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page(fmt.Sprintf("#%d v%d commits", ch.Number, v.Number))
	p.Link(changeHref(rc, ch), "back to the change")
	p.Blank()
	for _, rev := range revs {
		p.Link(rc.base+"/commit/"+string(rev.ID), fmt.Sprintf("%s %s %s (%s)", date(rev.Author.When), short(string(rev.ID)), rev.Subject, rev.Author.Name))
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) changePatch(req *request, rc *repoCtx, ch *store.Change, v *store.ChangeVersion) {
	_ = req.w.Header(gemini.StatusSuccess, "text/x-patch; charset=utf-8")
	_ = rc.repo.FormatPatch(req.ctx, vcs.RevisionID(v.BaseRev), vcs.RevisionID(v.HeadRev), 32<<20, req.w)
}

func (h *Handler) changeInterdiff(req *request, rc *repoCtx, ch *store.Change, a, b *store.ChangeVersion) {
	text, truncated, err := rc.repo.RangeDiff(req.ctx, vcs.RevisionID(a.BaseRev), vcs.RevisionID(a.HeadRev), vcs.RevisionID(b.BaseRev), vcs.RevisionID(b.HeadRev), h.F.Config.Limits.MaxDiffBytes)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page(fmt.Sprintf("#%d interdiff v%d -> v%d", ch.Number, a.Number, b.Number))
	p.Link(changeHref(rc, ch), "back to the change")
	p.Blank()
	p.Pre("range-diff", text)
	if truncated {
		p.Text("(truncated)")
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) changeFeed(req *request, rc *repoCtx, ch *store.Change, atom bool) {
	events, err := h.F.Store.Events(req.ctx, store.EventQuery{Viewer: req.id.UserID(), RepoID: rc.acc.Repo.ID, Limit: 500})
	if err != nil {
		req.fail(err)
		return
	}
	want := changeHref(rc, ch)
	var mine []*store.Event
	for _, e := range events {
		if e.Path == want || strings.Contains(string(e.Payload), fmt.Sprintf(`"number":%d`, ch.Number)) && strings.HasPrefix(e.Kind, "change.") {
			mine = append(mine, e)
		}
		if len(mine) >= feedLimit {
			break
		}
	}
	h.writeFeed(req, fmt.Sprintf("%s/%s change #%d", rc.acc.Repo.Owner, rc.acc.Repo.Name, ch.Number), want, mine, atom)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// titanChanges handles Titan writes under /~o/r/changes/.
func (h *Handler) titanChanges(req *request, u *store.User, rc *repoCtx, rest []string) {
	if len(rest) != 2 {
		_ = gemini.NotFound(req.w)
		return
	}
	n, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	ch, err := h.F.LookupChange(req.ctx, rc.acc, n)
	if err != nil {
		req.fail(err)
		return
	}
	readBody := func(limit int64) (string, bool) {
		if req.Titan.Size == 0 {
			return "", true
		}
		return h.readTitanText(req, limit)
	}
	switch rest[1] {
	case "comment":
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if _, err := h.F.CommentChange(req.ctx, u, rc.acc, ch, body); err != nil {
			req.fail(err)
			return
		}
	case "review":
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if _, err := h.F.ReviewChange(req.ctx, u, rc.acc, ch, body); err != nil {
			if errors.Is(err, forge.ErrNotAcceptable) {
				_ = gemini.BadRequest(req.w, "first line must be approve, request-changes or comment")
				return
			}
			req.fail(err)
			return
		}
	case "edit":
		if req.Titan.Edit {
			if !(rc.acc.CanWrite() || ch.AuthorID == u.ID) {
				_ = gemini.Forbidden(req.w, "not permitted")
				return
			}
			_ = req.w.Header(gemini.StatusSuccess, "text/plain; charset=utf-8")
			fmt.Fprintf(req.w, "%s\n\n%s\n", ch.Title, ch.Body)
			return
		}
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if err := h.F.EditChange(req.ctx, u, rc.acc, ch, body); err != nil {
			req.fail(err)
			return
		}
	case "close", "reopen":
		body, ok := readBody(h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if err := h.F.CloseChange(req.ctx, u, rc.acc, ch, rest[1] == "close", body); err != nil {
			req.fail(err)
			return
		}
	case "merge":
		body, ok := readBody(64 << 10)
		if !ok {
			return
		}
		newID, err := h.F.MergeChange(req.ctx, u, rc.acc, ch, body)
		var me *forge.MergeErr
		if errors.As(err, &me) {
			p := req.page(fmt.Sprintf("Change #%d was not merged", ch.Number))
			if len(me.Conflicts) > 0 {
				p.Text("Conflicts in:")
				for _, c := range me.Conflicts {
					p.Item(c)
				}
				p.Textf("The author must rebase onto %s and push a new version:", ch.TargetBranch)
				p.Pre("rebase", fmt.Sprintf("git fetch origin %s && git rebase origin/%s && git push origin HEAD:refs/changes/%d", ch.TargetBranch, ch.TargetBranch, ch.Number))
			} else {
				p.Text(me.Reason)
			}
			p.Link(changeHref(rc, ch), "back to the change")
			req.send(p)
			return
		}
		if err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, rc.base+"/commit/"+string(newID))
		return
	default:
		_ = gemini.NotFound(req.w)
		return
	}
	_ = gemini.Redirect(req.w, changeHref(rc, ch))
}
