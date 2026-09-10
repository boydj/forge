package web

import (
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// Milestone 3 endpoints. Present so routing is complete; implemented in
// issues.go, changes.go and releases.go.

func (h *Handler) changeRoutes(req *request, rc *repoCtx, rest []string) {
	p := req.page("Changes of " + rc.acc.Repo.Owner + "/" + rc.acc.Repo.Name)
	p.Text("Change review arrives in the next milestone.")
	p.Link(rc.base+"/", "repository overview")
	req.send(p)
}

func (h *Handler) titanChanges(req *request, u *store.User, rc *repoCtx, rest []string) {
	_ = gemini.NotFound(req.w)
}
