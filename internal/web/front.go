package web

import (
	"fmt"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

func (h *Handler) front(req *request) {
	p := req.page(h.F.Config.Title)
	p.Text("A source forge on Gemini, Titan and Git over SSH.")
	p.Blank()
	if note, _ := h.F.Store.Setting(req.ctx, "announcement"); note != "" {
		p.Heading(2, "Notice")
		p.Text(note)
		p.Blank()
	}
	if req.id != nil && req.id.User != nil {
		p.Link("/~"+req.id.User.Name+"/", "your repositories")
		p.Link("/new", "create a repository")
	} else {
		p.Link("/account", "sign in or register with a client certificate")
	}
	p.Link("/feed", "activity feed")
	p.Blank()
	repos, err := h.F.Store.ListRepos(req.ctx, store.RepoListOptions{Viewer: req.id.UserID(), Limit: 30})
	if err != nil {
		req.fail(err)
		return
	}
	p.Heading(2, "Recently updated repositories")
	if len(repos) == 0 {
		p.Text("No repositories yet.")
	}
	for _, r := range repos {
		label := r.Owner + "/" + r.Name
		if r.Description != "" {
			label += " - " + r.Description
		}
		if r.Private {
			label += " (private)"
		}
		p.Link(fmt.Sprintf("/~%s/%s/", r.Owner, r.Name), label)
	}
	p.Blank()
	events, err := h.F.Store.Events(req.ctx, store.EventQuery{Viewer: req.id.UserID(), Limit: 15})
	if err == nil && len(events) > 0 {
		p.Heading(2, "Recent activity")
		for _, e := range events {
			p.Link(e.Path, when(e.CreatedAt)+" "+e.Subject)
		}
	}
	h.footer(p, req)
	req.send(p)
}

// newRepo creates a repository via an INPUT prompt for the name.
func (h *Handler) newRepo(req *request) {
	u := req.requireUser()
	if u == nil {
		return
	}
	name, ok := req.action(h, "Repository name (lowercase letters, digits, . _ -)", false)
	if !ok {
		return
	}
	r, err := h.F.CreateRepo(req.ctx, u, forge.CreateRepoOptions{Name: name})
	if err != nil {
		req.fail(err)
		return
	}
	_ = gemini.Redirect(req.w, fmt.Sprintf("/~%s/%s/", r.Owner, r.Name))
}
