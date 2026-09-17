package web

import (
	"fmt"
	"strings"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/pkg/gemini"
)

// Settings routes (repository admins only). State changes use INPUT with
// a typed value; the description also accepts a Titan upload.
//
//	/~o/r/settings
//	/~o/r/settings/description?text        (Titan too)
//	/~o/r/settings/visibility?public|private
//	/~o/r/settings/archive?archive|unarchive
//	/~o/r/settings/branch?name
//	/~o/r/settings/collaborators/add?user role
//	/~o/r/settings/collaborators/remove?user
//	/~o/r/settings/delete?owner/name
func (h *Handler) repoSettings(req *request, rc *repoCtx, rest []string) {
	u := req.requireUser()
	if u == nil {
		return
	}
	if !rc.acc.CanAdmin() {
		_ = gemini.Forbidden(req.w, "repository administrators only")
		return
	}
	r := rc.acc.Repo
	if len(rest) == 0 {
		h.settingsPage(req, rc)
		return
	}
	prompts := map[string]string{
		"description": "Description (one line; type '-' to clear)",
		"visibility":  "Type \"public\" or \"private\"",
		"archive":     "Type \"archive\" to make the repository read-only, or \"unarchive\"",
		"branch":      "Default branch name",
		"delete":      fmt.Sprintf("Type \"%s/%s\" to delete this repository (restorable by an administrator for %s)", r.Owner, r.Name, forge.DeleteRetention),
	}
	prompt := prompts[rest[0]]
	if rest[0] == "collaborators" && len(rest) == 2 {
		prompt = map[string]string{"add": "Username and role, e.g. \"bob write\" (roles: read, write, admin)", "remove": "Username to remove"}[rest[1]]
	}
	if prompt == "" {
		_ = gemini.NotFound(req.w)
		return
	}
	q, ok := req.action(h, prompt, false)
	if !ok {
		return
	}
	done := func(err error) {
		if err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, rc.base+"/settings")
	}
	switch rest[0] {
	case "description":
		if q == "-" {
			q = ""
		}
		done(h.F.UpdateRepo(req.ctx, u, r, forge.UpdateRepoOptions{Description: &q}))
	case "visibility":
		if q != "public" && q != "private" {
			_ = gemini.Input(req.w, "Not understood. Type \"public\" or \"private\"")
			return
		}
		priv := q == "private"
		done(h.F.UpdateRepo(req.ctx, u, r, forge.UpdateRepoOptions{Private: &priv}))
	case "archive":
		if q != "archive" && q != "unarchive" {
			_ = gemini.Input(req.w, "Not understood. Type \"archive\" or \"unarchive\"")
			return
		}
		arch := q == "archive"
		done(h.F.UpdateRepo(req.ctx, u, r, forge.UpdateRepoOptions{Archived: &arch}))
	case "branch":
		done(h.F.UpdateRepo(req.ctx, u, r, forge.UpdateRepoOptions{DefaultBranch: &q}))
	case "collaborators":
		if len(rest) != 2 {
			_ = gemini.NotFound(req.w)
			return
		}
		switch rest[1] {
		case "add":
			name, role, _ := strings.Cut(q, " ")
			if name == "" || (role != "read" && role != "write" && role != "admin") {
				_ = gemini.Input(req.w, "Not understood. Username and role, e.g. \"bob write\" (roles: read, write, admin)")
				return
			}
			done(h.F.SetCollaborator(req.ctx, u, r, name, store.Role(role)))
		case "remove":
			done(h.F.SetCollaborator(req.ctx, u, r, q, store.RoleNone))
		default:
			_ = gemini.NotFound(req.w)
		}
	case "delete":
		if err := h.F.DeleteRepo(req.ctx, u, r, q); err != nil {
			if err == forge.ErrNotAcceptable {
				_ = gemini.Input(req.w, fmt.Sprintf("Name did not match. Type \"%s/%s\" to delete", r.Owner, r.Name))
				return
			}
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, "/~"+r.Owner+"/")
	default:
		_ = gemini.NotFound(req.w)
	}
}

func (h *Handler) settingsPage(req *request, rc *repoCtx) {
	r := rc.acc.Repo
	p := req.page("Settings of " + r.Owner + "/" + r.Name)
	p.Textf("Description: %s", orDash(r.Description))
	p.Link(rc.base+"/settings/description", "change description")
	p.Textf("Titan upload accepted at %s", h.F.Config.TitanURL(rc.base+"/settings/description"))
	p.Blank()
	vis := "public"
	if r.Private {
		vis = "private"
	}
	p.Textf("Visibility: %s", vis)
	p.Link(rc.base+"/settings/visibility", "change visibility")
	p.Blank()
	if r.Archived {
		p.Text("Archived: yes (read-only)")
	} else {
		p.Text("Archived: no")
	}
	p.Link(rc.base+"/settings/archive", "archive or unarchive")
	p.Blank()
	p.Textf("Default branch: %s", r.DefaultBranch)
	p.Link(rc.base+"/settings/branch", "change default branch")
	p.Blank()
	p.Heading(2, "Collaborators")
	collabs, _ := h.F.Store.ListCollaborators(req.ctx, r.ID)
	for _, c := range collabs {
		p.Textf("%s: %s", c.Name, c.Role)
	}
	if len(collabs) == 0 {
		p.Text("None.")
	}
	p.Link(rc.base+"/settings/collaborators/add", "add or change a collaborator")
	p.Link(rc.base+"/settings/collaborators/remove", "remove a collaborator")
	p.Blank()
	p.Heading(2, "Danger")
	p.Link(rc.base+"/settings/delete", "delete this repository")
	p.Blank()
	p.Link(rc.base+"/", "repository overview")
	h.footer(p, req)
	req.send(p)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (h *Handler) titanSettings(req *request, u *store.User, rc *repoCtx, rest []string) {
	if len(rest) != 1 || rest[0] != "description" {
		_ = gemini.NotFound(req.w)
		return
	}
	if !rc.acc.CanAdmin() {
		_ = gemini.Forbidden(req.w, "repository administrators only")
		return
	}
	body, ok := h.readTitanText(req, 4096)
	if !ok {
		return
	}
	desc := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if err := h.F.UpdateRepo(req.ctx, u, rc.acc.Repo, forge.UpdateRepoOptions{Description: &desc}); err != nil {
		req.fail(err)
		return
	}
	_ = gemini.Redirect(req.w, rc.base+"/settings")
}
