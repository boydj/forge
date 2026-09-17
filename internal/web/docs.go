package web

import (
	"errors"
	"io"
	"mime"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"as215520.net/forge/internal/vcs"
	"as215520.net/forge/pkg/config"
	"as215520.net/forge/pkg/gemini"
)

// docs serves the documentation site: the directory named by [docs] of a
// public repository hosted on this forge, read at its default branch and
// rendered as gemtext under /docs/. Markdown and gemtext files render inline
// with relative links rebased so that cross-references between documents
// keep working; other text renders preformatted; binaries are served raw.
// Publishing a document is a git push to that repository.
func (h *Handler) docs(req *request, rest []string, trailing bool) {
	cfg := h.F.Config.Docs
	owner, name, ok := strings.Cut(cfg.Repo, "/")
	if cfg.Repo == "" || !ok {
		_ = gemini.NotFound(req.w)
		return
	}
	// Anonymous lookup on purpose: only a public repository can publish the
	// site documentation, whatever certificate the visitor presents.
	acc, err := h.F.LookupRepo(req.ctx, nil, owner, name)
	if err != nil {
		req.fail(err)
		return
	}
	repo, err := h.F.Open(acc.Repo)
	if err != nil {
		req.fail(err)
		return
	}
	ref := cfg.Ref
	if ref == "" {
		ref = acc.Repo.DefaultBranch
	}
	id, err := repo.Resolve(req.ctx, ref)
	if err != nil {
		// Empty repository or a ref that does not exist yet.
		_ = gemini.NotFound(req.w)
		return
	}
	// Normalise dot segments so "../" links between documents resolve to
	// the canonical URL; anything escaping the tree is simply not found.
	rel := strings.Join(rest, "/")
	if rel != "" {
		clean := path.Clean("/" + rel)
		if clean == "/" || strings.HasPrefix(clean, "/../") {
			_ = gemini.NotFound(req.w)
			return
		}
		if clean[1:] != rel {
			target := "/docs" + clean
			if trailing {
				target += "/"
			}
			_ = req.w.Header(gemini.StatusRedirectPermanent, target)
			return
		}
	}
	full := gitPath(cfg.Path, rel)
	entries, err := repo.Tree(req.ctx, id, full)
	if errors.Is(err, vcs.ErrNotDir) {
		h.docsFile(req, repo, id, cfg, ref, rel)
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
	h.docsDir(req, repo, id, cfg, ref, rel, entries)
}

// gitPath joins repository path components without URL escaping (the
// joinPath helper escapes for URLs and must not be used for tree lookups).
func gitPath(dir, rel string) string {
	switch {
	case dir == "":
		return rel
	case rel == "":
		return dir
	}
	return dir + "/" + rel
}

// docsBase is the URL of the directory containing rel (with trailing slash),
// used to rebase relative links.
func docsBase(dir string) string {
	if dir == "" || dir == "." {
		return "/docs/"
	}
	return "/docs/" + dir + "/"
}

// docsSource links back to the repository view of a path.
func docsSource(cfg config.Docs, ref, full string, dir bool) string {
	s := "/~" + cfg.Repo + "/tree/" + ref + "/" + full
	if dir && full != "" {
		s += "/"
	}
	return s
}

// docsDir renders a directory: its README inline (if any) and a listing.
func (h *Handler) docsDir(req *request, repo vcs.Repository, id vcs.RevisionID, cfg config.Docs, ref, rel string, entries []vcs.TreeEntry) {
	base := docsBase(rel)
	present := map[string]bool{}
	for _, e := range entries {
		if e.Kind == vcs.EntryFile || e.Kind == vcs.EntryExecutable {
			present[e.Name] = true
		}
	}
	readme := ""
	for _, n := range readmeNames {
		if present[n] {
			readme = n
			break
		}
	}
	p := req.page("")
	rendered := false
	if readme != "" {
		if text, ok := h.docsText(req, repo, id, gitPath(cfg.Path, gitPath(rel, readme))); ok {
			p.Raw(renderDoc(readme, text, base))
			rendered = true
		}
	}
	if !rendered {
		title := "Documentation"
		if rel != "" {
			title += ": " + rel
		}
		p.Heading(1, title)
	}
	p.Blank()
	p.Heading(2, "Contents")
	if rel != "" {
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		p.Link(docsBase(parent), "..")
	}
	for _, e := range entries {
		if e.Name == readme {
			continue
		}
		switch e.Kind {
		case vcs.EntryDir:
			p.Link(base+url.PathEscape(e.Name)+"/", e.Name+"/")
		case vcs.EntryFile, vcs.EntryExecutable:
			p.Link(base+url.PathEscape(e.Name), e.Name)
		}
	}
	p.Blank()
	p.Link(docsSource(cfg, ref, gitPath(cfg.Path, rel), true), "source of these pages")
	h.footer(p, req)
	req.send(p)
}

// docsFile renders one document, or serves it raw when it is not text.
func (h *Handler) docsFile(req *request, repo vcs.Repository, id vcs.RevisionID, cfg config.Docs, ref, rel string) {
	full := gitPath(cfg.Path, rel)
	blob, err := repo.Blob(req.ctx, id, full)
	if err != nil {
		_ = gemini.NotFound(req.w)
		return
	}
	defer blob.Close()
	limit := h.F.Config.Limits.MaxRenderBytes
	data, err := io.ReadAll(io.LimitReader(blob.Reader, limit+1))
	if err != nil {
		req.fail(err)
		return
	}
	name := path.Base(rel)
	if !utf8.Valid(data) {
		mt := mime.TypeByExtension(path.Ext(name))
		if mt == "" {
			mt = "application/octet-stream"
		}
		_ = req.w.Header(gemini.StatusSuccess, mt)
		_, _ = req.w.Write(data)
		_, _ = io.Copy(req.w, blob.Reader)
		return
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	p := req.page("")
	p.Raw(renderDoc(name, string(data), docsBase(dir)))
	if truncated {
		p.Blank()
		p.Link("/~"+cfg.Repo+"/raw/"+ref+"/"+full, "(truncated; full file)")
	}
	p.Blank()
	p.Link(docsBase(dir), "documentation index")
	p.Link(docsSource(cfg, ref, full, false), "source of this page")
	h.footer(p, req)
	req.send(p)
}

// docsText reads a text document bounded by the render limit.
func (h *Handler) docsText(req *request, repo vcs.Repository, id vcs.RevisionID, full string) (string, bool) {
	blob, err := repo.Blob(req.ctx, id, full)
	if err != nil {
		return "", false
	}
	defer blob.Close()
	data, err := io.ReadAll(io.LimitReader(blob.Reader, h.F.Config.Limits.MaxRenderBytes))
	if err != nil || !utf8.Valid(data) {
		return "", false
	}
	return string(data), true
}

// renderDoc converts a document to gemtext by file type. base is the URL of
// the directory holding it, for relative links.
func renderDoc(name, text, base string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".gmi") || strings.HasSuffix(lower, ".gemini"):
		return rebaseGemtextLinks(text, base)
	case strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown"):
		return MarkdownToGemtext(text, base)
	default:
		p := gemini.NewPage()
		p.Pre(name, text)
		return p.String()
	}
}
