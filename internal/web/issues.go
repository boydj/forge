package web

import (
	"fmt"
	"strconv"
	"strings"

	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// Issue routes:
//
//	/~o/r/issues/            list (open); ?closed lists closed
//	/~o/r/issues/feed        gemfeed of issue events
//	/~o/r/issues/new         Gemini: instructions; Titan: create (title line + body)
//	/~o/r/issues/<n>         issue and comments
//	/~o/r/issues/<n>/comment Titan: add comment
//	/~o/r/issues/<n>/edit    Titan: replace title/body; Titan ;edit returns raw text
//	/~o/r/issues/<n>/close   Gemini INPUT confirmation
//	/~o/r/issues/<n>/reopen  Gemini INPUT confirmation
//	/~o/r/issues/<n>/comments/<id>/edit|delete
func (h *Handler) issueRoutes(req *request, rc *repoCtx, rest []string) {
	trailing := strings.HasSuffix(req.Path(), "/")
	if len(rest) == 0 {
		if !trailing {
			_ = req.w.Header(gemini.StatusRedirectPermanent, rc.base+"/issues/")
			return
		}
		h.issueList(req, rc)
		return
	}
	switch rest[0] {
	case "feed":
		h.feedRepo(req, rc, []string{store.EventIssueOpen, store.EventIssueClose, store.EventIssueReopen, store.EventComment}, false)
		return
	case "atom.xml":
		h.feedRepo(req, rc, []string{store.EventIssueOpen, store.EventIssueClose, store.EventIssueReopen, store.EventComment}, true)
		return
	case "new":
		h.issueNewPage(req, rc)
		return
	}
	n, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || n <= 0 {
		_ = gemini.NotFound(req.w)
		return
	}
	is, err := h.F.LookupIssue(req.ctx, rc.acc, n)
	if err != nil {
		req.fail(err)
		return
	}
	if len(rest) == 1 {
		h.issuePage(req, rc, is)
		return
	}
	switch rest[1] {
	case "close", "reopen":
		h.issueSetState(req, rc, is, rest[1] == "close")
	case "comments":
		if len(rest) != 4 || rest[3] != "delete" {
			_ = gemini.NotFound(req.w)
			return
		}
		id, err := strconv.ParseInt(rest[2], 10, 64)
		if err != nil {
			_ = gemini.NotFound(req.w)
			return
		}
		c, err := h.F.Store.CommentByID(req.ctx, id)
		if err != nil || c.TargetKind != "issue" || c.TargetID != is.ID {
			_ = gemini.NotFound(req.w)
			return
		}
		h.commentDelete(req, rc, c, issueHref(rc, is))
	case "comment", "edit":
		p := req.page("Issue #" + strconv.FormatInt(is.Number, 10))
		p.Textf("Upload text with Titan to %s", h.F.Config.TitanURL(req.Path()))
		if rest[1] == "edit" {
			p.Text("First line is the title, the rest is the body.")
		}
		p.Link(issueHref(rc, is), "back to the issue")
		req.send(p)
	default:
		_ = gemini.NotFound(req.w)
	}
}

func issueHref(rc *repoCtx, is *store.Issue) string {
	return fmt.Sprintf("%s/issues/%d", rc.base, is.Number)
}

func (h *Handler) issueList(req *request, rc *repoCtx) {
	r := rc.acc.Repo
	state := "open"
	if req.Query() == "closed" {
		state = "closed"
	}
	issues, err := h.F.Store.ListIssues(req.ctx, r.ID, state, 100, 0)
	if err != nil {
		req.fail(err)
		return
	}
	open, closed, _ := h.F.Store.CountIssues(req.ctx, r.ID)
	p := req.page(fmt.Sprintf("Issues of %s/%s", r.Owner, r.Name))
	p.Link(rc.base+"/", "repository overview")
	if state == "open" {
		p.Link(rc.base+"/issues/?closed", fmt.Sprintf("closed issues (%d)", closed))
	} else {
		p.Link(rc.base+"/issues/", fmt.Sprintf("open issues (%d)", open))
	}
	p.Link(rc.base+"/issues/new", "open a new issue")
	p.Link(rc.base+"/issues/feed", "issues feed")
	p.Blank()
	p.Heading(2, fmt.Sprintf("%s issues (%d)", strings.ToUpper(state[:1])+state[1:], len(issues)))
	for _, is := range issues {
		label := fmt.Sprintf("#%d %s (%s, %s", is.Number, is.Title, is.Author, date(is.UpdatedAt))
		if is.Comments > 0 {
			label += fmt.Sprintf(", %d comments", is.Comments)
		}
		p.Link(issueHref(rc, is), label+")")
	}
	if len(issues) == 0 {
		p.Text("None.")
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) issueNewPage(req *request, rc *repoCtx) {
	p := req.page("New issue in " + rc.acc.Repo.Owner + "/" + rc.acc.Repo.Name)
	if rc.acc.Repo.Archived {
		p.Text("This repository is archived; issues are read-only.")
	} else {
		p.Text("Upload the issue text with Titan (in Lagrange: Upload / Edit page with Titan). The first line is the title, the rest is the body. Your client certificate identifies you.")
		p.Pre("titan", h.F.Config.TitanURL(rc.base+"/issues/new"))
	}
	p.Link(rc.base+"/issues/", "back to issues")
	req.send(p)
}

func (h *Handler) issuePage(req *request, rc *repoCtx, is *store.Issue) {
	comments, err := h.F.Store.ListComments(req.ctx, "issue", is.ID)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page(fmt.Sprintf("#%d %s", is.Number, is.Title))
	p.Textf("%s issue opened by %s on %s in %s/%s", is.State, is.Author, date(is.CreatedAt), rc.acc.Repo.Owner, rc.acc.Repo.Name)
	if !is.ClosedAt.IsZero() && is.State == "closed" {
		p.Textf("Closed on %s.", date(is.ClosedAt))
	}
	p.Blank()
	if is.Body != "" {
		p.Raw(userGemtext(is.Body))
		p.Blank()
	}
	for _, c := range comments {
		p.Heading(2, fmt.Sprintf("%s, %s", c.Author, when(c.CreatedAt)))
		p.Raw(userGemtext(c.Body))
		if req.id.User != nil && (c.AuthorID == req.id.UserID() || rc.acc.CanWrite()) {
			p.Link(fmt.Sprintf("%s/comments/%d/delete", issueHref(rc, is), c.ID), "delete this comment")
		}
		p.Blank()
	}
	p.Heading(2, "Actions")
	if !rc.acc.Repo.Archived {
		p.Textf("Comment: upload text with Titan to %s", h.F.Config.TitanURL(issueHref(rc, is)+"/comment"))
		if req.id.User != nil && (rc.acc.CanWrite() || is.AuthorID == req.id.UserID()) {
			if is.State == "open" {
				p.Link(issueHref(rc, is)+"/close", "close this issue")
			} else {
				p.Link(issueHref(rc, is)+"/reopen", "reopen this issue")
			}
			p.Textf("Edit: upload title and body with Titan to %s", h.F.Config.TitanURL(issueHref(rc, is)+"/edit"))
		}
	}
	p.Link(rc.base+"/issues/", "all issues")
	h.footer(p, req)
	req.send(p)
}

// issueSetState closes/reopens after an INPUT confirmation so that a bare
// link click never changes state.
func (h *Handler) issueSetState(req *request, rc *repoCtx, is *store.Issue, closed bool) {
	u := req.requireUser()
	if u == nil {
		return
	}
	verb := "reopen"
	if closed {
		verb = "close"
	}
	q := strings.TrimSpace(req.Query())
	if q == "" {
		_ = gemini.Input(req.w, fmt.Sprintf("Type %q to %s issue #%d, optionally followed by a comment", verb, verb, is.Number))
		return
	}
	word, comment, _ := strings.Cut(q, " ")
	if !strings.EqualFold(word, verb) {
		_ = gemini.Input(req.w, fmt.Sprintf("Not confirmed. Type %q to %s issue #%d", verb, verb, is.Number))
		return
	}
	if err := h.F.SetIssueState(req.ctx, u, rc.acc, is, closed); err != nil {
		req.fail(err)
		return
	}
	if comment = strings.TrimSpace(comment); comment != "" {
		if _, err := h.F.Comment(req.ctx, u, rc.acc, "issue", is.ID, is.Number, is.Title, comment); err != nil {
			req.fail(err)
			return
		}
	}
	_ = gemini.Redirect(req.w, issueHref(rc, is))
}

// titanIssues handles Titan writes under /~o/r/issues/.
func (h *Handler) titanIssues(req *request, u *store.User, rc *repoCtx, rest []string) {
	if len(rest) == 1 && rest[0] == "new" {
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		is, err := h.F.OpenIssue(req.ctx, u, rc.acc, body)
		if err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, issueHref(rc, is))
		return
	}
	if len(rest) < 2 {
		_ = gemini.NotFound(req.w)
		return
	}
	n, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	is, err := h.F.LookupIssue(req.ctx, rc.acc, n)
	if err != nil {
		req.fail(err)
		return
	}
	switch {
	case rest[1] == "comment" && len(rest) == 2:
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if _, err := h.F.Comment(req.ctx, u, rc.acc, "issue", is.ID, is.Number, is.Title, body); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, issueHref(rc, is))
	case rest[1] == "edit" && len(rest) == 2:
		if req.Titan.Edit {
			_ = req.w.Header(gemini.StatusSuccess, "text/plain; charset=utf-8")
			fmt.Fprintf(req.w, "%s\n\n%s\n", is.Title, is.Body)
			return
		}
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if err := h.F.EditIssue(req.ctx, u, rc.acc, is, body); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, issueHref(rc, is))
	case rest[1] == "comments" && len(rest) == 4:
		id, err := strconv.ParseInt(rest[2], 10, 64)
		if err != nil {
			_ = gemini.NotFound(req.w)
			return
		}
		c, err := h.F.Store.CommentByID(req.ctx, id)
		if err != nil || c.TargetKind != "issue" || c.TargetID != is.ID {
			_ = gemini.NotFound(req.w)
			return
		}
		switch rest[3] {
		case "edit":
			if req.Titan.Edit {
				_ = req.w.Header(gemini.StatusSuccess, "text/plain; charset=utf-8")
				fmt.Fprintln(req.w, c.Body)
				return
			}
			body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
			if !ok {
				return
			}
			if err := h.F.EditComment(req.ctx, u, rc.acc, c, body); err != nil {
				req.fail(err)
				return
			}
		case "delete":
			if err := h.F.DeleteComment(req.ctx, u, rc.acc, c); err != nil {
				req.fail(err)
				return
			}
		default:
			_ = gemini.NotFound(req.w)
			return
		}
		_ = gemini.Redirect(req.w, issueHref(rc, is))
	default:
		_ = gemini.NotFound(req.w)
	}
}

// commentDelete handles the Gemini INPUT-confirmed comment deletion.
func (h *Handler) commentDelete(req *request, rc *repoCtx, c *store.Comment, back string) {
	u := req.requireUser()
	if u == nil {
		return
	}
	if q := strings.TrimSpace(req.Query()); !strings.EqualFold(q, "delete") {
		_ = gemini.Input(req.w, "Type \"delete\" to remove this comment")
		return
	}
	if err := h.F.DeleteComment(req.ctx, u, rc.acc, c); err != nil {
		req.fail(err)
		return
	}
	_ = gemini.Redirect(req.w, back)
}

// userGemtext renders user-authored text. Gemtext structure written by the
// user is kept (headings are capped at level 3, link lines are allowed but
// marked) so issues and comments can carry links and code blocks.
func userGemtext(text string) string {
	var out strings.Builder
	inPre := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "```"):
			inPre = !inPre
			out.WriteString("```\n")
		case inPre:
			out.WriteString(l + "\n")
		case strings.HasPrefix(l, "=>"):
			target, label, _ := strings.Cut(strings.TrimSpace(l[2:]), " ")
			target = sanitizeURL(target)
			if target == "" || !(strings.HasPrefix(target, "gemini://") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "titan://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://")) {
				out.WriteString(" " + l + "\n")
				continue
			}
			label = strings.TrimSpace(label)
			if label == "" {
				label = target
			}
			out.WriteString("=> " + target + " " + label + " [user link]\n")
		case strings.HasPrefix(l, "#"):
			// Headings inside user text are demoted below the page structure.
			out.WriteString("### " + strings.TrimSpace(strings.TrimLeft(l, "#")) + "\n")
		default:
			out.WriteString(l + "\n")
		}
	}
	if inPre {
		out.WriteString("```\n")
	}
	return out.String()
}
