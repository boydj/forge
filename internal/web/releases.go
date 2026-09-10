package web

import (
	"fmt"
	"io"
	"strings"

	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// Release routes:
//
//	/~o/r/releases/                 list
//	/~o/r/releases/feed             gemfeed of releases
//	/~o/r/releases/new              Gemini: instructions; Titan: create (tag / title / notes)
//	/~o/r/releases/<tag>            release page
//	/~o/r/releases/<tag>/edit       Titan: replace title/notes; ;edit raw
//	/~o/r/releases/<tag>/delete     INPUT confirmation
//	/~o/r/releases/<tag>/assets/<name>          Gemini: download; Titan: upload
//	/~o/r/releases/<tag>/assets/<name>/delete   INPUT confirmation
func (h *Handler) releaseRoutes(req *request, rc *repoCtx, rest []string) {
	trailing := strings.HasSuffix(req.Path(), "/")
	if len(rest) == 0 {
		if !trailing {
			_ = req.w.Header(gemini.StatusRedirectPermanent, rc.base+"/releases/")
			return
		}
		h.releaseList(req, rc)
		return
	}
	switch rest[0] {
	case "feed":
		h.feedRepo(req, rc, []string{store.EventRelease, store.EventReleaseAsset}, false)
		return
	case "atom.xml":
		h.feedRepo(req, rc, []string{store.EventRelease, store.EventReleaseAsset}, true)
		return
	case "new":
		p := req.page("New release in " + rc.acc.Repo.Owner + "/" + rc.acc.Repo.Name)
		p.Text("Push a tag first, then upload the release notes with Titan. First line: tag name. Second line: title. Remaining lines: notes.")
		p.Pre("titan", h.F.Config.TitanURL(rc.base+"/releases/new"))
		p.Link(rc.base+"/releases/", "back to releases")
		req.send(p)
		return
	}
	rel, err := h.F.LookupRelease(req.ctx, rc.acc, rest[0])
	if err != nil {
		req.fail(err)
		return
	}
	if len(rest) == 1 {
		h.releasePage(req, rc, rel)
		return
	}
	switch {
	case rest[1] == "delete" && len(rest) == 2:
		u := req.requireUser()
		if u == nil {
			return
		}
		q, ok := req.action(h, fmt.Sprintf("Type \"delete\" to remove release %s and its assets", rel.Tag), false)
		if !ok {
			return
		}
		if !strings.EqualFold(q, "delete") {
			_ = gemini.Input(req.w, "Not confirmed. Type \"delete\" to remove the release")
			return
		}
		if err := h.F.DeleteRelease(req.ctx, u, rc.acc, rel); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, rc.base+"/releases/")
	case rest[1] == "edit" && len(rest) == 2:
		p := req.page("Edit release " + rel.Tag)
		p.Textf("Upload title (first line) and notes with Titan to %s", h.F.Config.TitanURL(req.Path()))
		p.Link(releaseHref(rc, rel), "back to the release")
		req.send(p)
	case rest[1] == "assets" && len(rest) >= 3:
		a, err := h.F.Store.ReleaseAsset(req.ctx, rel.ID, rest[2])
		if err != nil {
			_ = gemini.NotFound(req.w)
			return
		}
		if len(rest) == 4 && rest[3] == "delete" {
			u := req.requireUser()
			if u == nil {
				return
			}
			q, ok := req.action(h, fmt.Sprintf("Type \"delete\" to remove asset %s", a.Name), false)
			if !ok {
				return
			}
			if !strings.EqualFold(q, "delete") {
				_ = gemini.Input(req.w, "Not confirmed. Type \"delete\" to remove the asset")
				return
			}
			if err := h.F.RemoveAsset(req.ctx, u, rc.acc, rel, a); err != nil {
				req.fail(err)
				return
			}
			_ = gemini.Redirect(req.w, releaseHref(rc, rel))
			return
		}
		if len(rest) != 3 {
			_ = gemini.NotFound(req.w)
			return
		}
		fh, err := h.F.OpenAsset(rc.acc, rel, a)
		if err != nil {
			_ = gemini.NotFound(req.w)
			return
		}
		defer fh.Close()
		_ = req.w.Header(gemini.StatusSuccess, a.MIME)
		_, _ = io.Copy(req.w, fh)
	default:
		_ = gemini.NotFound(req.w)
	}
}

func releaseHref(rc *repoCtx, rel *store.Release) string {
	return rc.base + "/releases/" + rel.Tag
}

func (h *Handler) releaseList(req *request, rc *repoCtx) {
	r := rc.acc.Repo
	rels, err := h.F.Store.ListReleases(req.ctx, r.ID, 100)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page("Releases of " + r.Owner + "/" + r.Name)
	p.Link(rc.base+"/", "repository overview")
	p.Link(rc.base+"/releases/feed", "releases feed")
	if rc.acc.CanWrite() {
		p.Link(rc.base+"/releases/new", "publish a release")
	}
	p.Blank()
	for _, rel := range rels {
		p.Link(releaseHref(rc, rel), fmt.Sprintf("%s %s - %s", date(rel.CreatedAt), rel.Tag, rel.Title))
	}
	if len(rels) == 0 {
		p.Text("No releases yet.")
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) releasePage(req *request, rc *repoCtx, rel *store.Release) {
	p := req.page(rel.Tag + ": " + rel.Title)
	p.Textf("Released by %s on %s.", rel.Author, date(rel.CreatedAt))
	p.Link(rc.base+"/commit/"+rel.Tag, "tagged commit")
	p.Link(rc.base+"/tree/"+rel.Tag+"/", "browse files at this tag")
	p.Blank()
	if rel.Body != "" {
		p.Raw(userGemtext(rel.Body))
		p.Blank()
	}
	p.Heading(2, "Assets")
	for _, a := range rel.Assets {
		p.Link(releaseHref(rc, rel)+"/assets/"+a.Name, fmt.Sprintf("%s (%s, %s)", a.Name, humanBytes(a.Size), a.MIME))
		p.Text("sha256 " + a.SHA256)
		if rc.acc.CanWrite() {
			p.Link(releaseHref(rc, rel)+"/assets/"+a.Name+"/delete", "delete "+a.Name)
		}
	}
	if len(rel.Assets) == 0 {
		p.Text("None.")
	}
	if rc.acc.CanWrite() && !rc.acc.Repo.Archived {
		p.Blank()
		p.Heading(2, "Maintain")
		p.Textf("Upload an asset with Titan to %s (set the MIME type; max %s)", h.F.Config.TitanURL(releaseHref(rc, rel)+"/assets/FILENAME"), humanBytes(h.F.Config.Limits.MaxAssetBytes))
		p.Textf("Edit notes: upload with Titan to %s", h.F.Config.TitanURL(releaseHref(rc, rel)+"/edit"))
		p.Link(releaseHref(rc, rel)+"/delete", "delete this release")
	}
	p.Link(rc.base+"/releases/", "all releases")
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) titanReleases(req *request, u *store.User, rc *repoCtx, rest []string) {
	if len(rest) == 1 && rest[0] == "new" {
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		rel, err := h.F.CreateRelease(req.ctx, u, rc.acc, body)
		if err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, releaseHref(rc, rel))
		return
	}
	if len(rest) < 2 {
		_ = gemini.NotFound(req.w)
		return
	}
	rel, err := h.F.LookupRelease(req.ctx, rc.acc, rest[0])
	if err != nil {
		req.fail(err)
		return
	}
	switch {
	case rest[1] == "edit" && len(rest) == 2:
		if req.Titan.Edit {
			if !rc.acc.CanWrite() {
				_ = gemini.Forbidden(req.w, "not permitted")
				return
			}
			_ = req.w.Header(gemini.StatusSuccess, "text/plain; charset=utf-8")
			fmt.Fprintf(req.w, "%s\n\n%s\n", rel.Title, rel.Body)
			return
		}
		body, ok := h.readTitanText(req, h.F.Config.Limits.MaxTextBytes)
		if !ok {
			return
		}
		if err := h.F.EditRelease(req.ctx, u, rc.acc, rel, body); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, releaseHref(rc, rel))
	case rest[1] == "assets" && len(rest) == 3:
		if req.Titan.Size > h.F.Config.Limits.MaxAssetBytes {
			_ = req.w.Header(gemini.StatusPermanentFailure, fmt.Sprintf("asset too large (limit %d bytes)", h.F.Config.Limits.MaxAssetBytes))
			return
		}
		if _, err := h.F.AddAsset(req.ctx, u, rc.acc, rel, rest[2], req.Titan.MIME, req.Titan.Size, req.Body); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, releaseHref(rc, rel))
	default:
		_ = gemini.NotFound(req.w)
	}
}
