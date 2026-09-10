package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// account handles /account and children. Identity flows:
//
//	no certificate            -> 60 asks for one (client creates/selects it)
//	unknown certificate       -> INPUT for a username, or enrolment code entry
//	known certificate         -> account page
func (h *Handler) account(req *request, rest []string) {
	if req.Certificate == nil {
		_ = gemini.CertRequired(req.w, "Create or select a client certificate to identify yourself")
		return
	}
	if req.id.User == nil {
		h.register(req, rest)
		return
	}
	u := req.id.User
	if len(rest) == 0 {
		h.accountPage(req, u)
		return
	}
	switch rest[0] {
	case "keys":
		h.accountKeys(req, u, rest[1:])
	case "certs":
		h.accountCerts(req, u, rest[1:])
	case "profile":
		h.accountProfile(req, u)
	default:
		_ = gemini.NotFound(req.w)
	}
}

func (h *Handler) register(req *request, rest []string) {
	if len(rest) > 0 && rest[0] == "enrol" {
		h.enrol(req)
		return
	}
	q := req.Query()
	if q == "" {
		p := req.page("Register")
		p.Textf("This certificate (%s) is not registered.", short(req.id.SPKI))
		p.Blank()
		p.Link("/account?", "Create a new account with this certificate")
		p.Link("/account/enrol", "Add this certificate to an existing account with an enrolment code")
		p.Blank()
		p.Text("Tip: create the identity for the whole site (not only this page) so it is sent for every path.")
		req.send(p)
		return
	}
	if q == "?" || strings.TrimSpace(q) == "" {
		_ = gemini.Input(req.w, "Choose a username (lowercase letters, digits, hyphens)")
		return
	}
	u, err := h.F.Register(req.ctx, req.id, q)
	if err != nil {
		if errors.Is(err, forge.ErrInvalidName) || errors.Is(err, forge.ErrReservedName) {
			_ = gemini.Input(req.w, "That name is not available. Choose a username (lowercase letters, digits, hyphens)")
			return
		}
		if errors.Is(err, forge.ErrExists) {
			_ = gemini.Input(req.w, "That name is taken. Choose another username")
			return
		}
		req.fail(err)
		return
	}
	h.Log.Info("registered", "user", u.Name, "spki", req.id.SPKI)
	_ = gemini.Redirect(req.w, "/account")
}

func (h *Handler) enrol(req *request) {
	code := req.Query()
	if code == "" {
		_ = req.w.Header(gemini.StatusSensitiveInput, "Enter the enrolment code generated from your existing device")
		return
	}
	tok, err := h.F.Store.ConsumeToken(req.ctx, "enrol", forge.HashToken(code))
	if err != nil {
		_ = req.w.Header(gemini.StatusSensitiveInput, "Code not valid or expired. Enter the enrolment code")
		return
	}
	u, err := h.F.Store.UserByID(req.ctx, tok.UserID)
	if err != nil {
		req.fail(err)
		return
	}
	if _, err := h.F.AddCertificate(req.ctx, u, req.id, "enrolled "+date(time.Now())); err != nil {
		req.fail(err)
		return
	}
	_ = gemini.Redirect(req.w, "/account")
}

func (h *Handler) accountPage(req *request, u *store.User) {
	p := req.page("Account: " + u.Name)
	if u.Admin {
		p.Text("You are a forge administrator.")
	}
	p.Link("/~"+u.Name+"/", "your public page")
	p.Link("/account/profile", "edit display name and bio")
	p.Link("/account/keys", "SSH keys for Git")
	p.Link("/account/certs", "certificates and devices")
	p.Link("/new", "create a repository")
	p.Blank()
	p.Heading(2, "Current certificate")
	p.Textf("Public key %s", req.id.SPKI)
	if !req.id.NotAfter.IsZero() {
		p.Textf("Expires %s", date(req.id.NotAfter))
	}
	h.footer(p, req)
	req.send(p)
}

func (h *Handler) accountProfile(req *request, u *store.User) {
	q := req.Query()
	if q == "" {
		_ = gemini.Input(req.w, "Display name and bio separated by ' | ' (leave empty to clear)")
		return
	}
	display, bio, _ := strings.Cut(q, "|")
	display, bio = strings.TrimSpace(display), strings.TrimSpace(bio)
	if len(display) > 64 || len(bio) > 512 {
		_ = gemini.BadRequest(req.w, "too long")
		return
	}
	if err := h.F.Store.UpdateUserProfile(req.ctx, u.ID, display, bio); err != nil {
		req.fail(err)
		return
	}
	_ = gemini.Redirect(req.w, "/account")
}

func (h *Handler) accountKeys(req *request, u *store.User, rest []string) {
	if len(rest) == 2 && rest[0] == "remove" {
		if !strings.EqualFold(strings.TrimSpace(req.Query()), "remove") {
			_ = gemini.Input(req.w, "Type \"remove\" to delete this SSH key")
			return
		}
		if err := h.F.RemoveSSHKey(req.ctx, u, "SHA256:"+rest[1]); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, "/account/keys")
		return
	}
	if len(rest) == 1 && rest[0] == "add" {
		q := req.Query()
		if q == "" {
			_ = gemini.Input(req.w, "Paste one SSH public key (authorized_keys format)")
			return
		}
		if _, err := h.F.AddSSHKeys(req.ctx, u, q); err != nil {
			if errors.Is(err, forge.ErrNotAcceptable) {
				_ = gemini.Input(req.w, "Not a valid ed25519/ecdsa/rsa public key. Paste one SSH public key")
				return
			}
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, "/account/keys")
		return
	}
	if len(rest) != 0 {
		_ = gemini.NotFound(req.w)
		return
	}
	keys, err := h.F.Store.ListSSHKeys(req.ctx, u.ID)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page("SSH keys")
	p.Text("Keys listed here may clone, fetch and push over SSH as " + u.Name + ".")
	p.Blank()
	if len(keys) == 0 {
		p.Text("No keys registered.")
	}
	for _, k := range keys {
		label := k.Label
		if label == "" {
			label = "(no label)"
		}
		p.Textf("%s %s added %s", k.KeyType, label, date(k.CreatedAt))
		p.Text(k.Fingerprint)
		p.Link("/account/keys/remove/"+strings.TrimPrefix(k.Fingerprint, "SHA256:"), "remove this key")
		p.Blank()
	}
	p.Heading(2, "Add keys")
	p.Link("/account/keys/add", "paste a single key")
	p.Textf("Or upload an authorized_keys file with Titan to %s", h.F.Config.TitanURL("/account/keys"))
	p.Blank()
	p.Heading(2, "Server host key")
	if hk := h.hostKeyLine(); hk != "" {
		p.Pre("known_hosts", hk)
	}
	h.footer(p, req)
	req.send(p)
}

// HostKeyLine is set by the daemon to the server's known_hosts line.
var HostKeyLine string

func (h *Handler) hostKeyLine() string { return HostKeyLine }

func (h *Handler) accountCerts(req *request, u *store.User, rest []string) {
	if len(rest) == 1 && rest[0] == "enrol-code" {
		// Generate a one-time code to enrol another device.
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			req.fail(err)
			return
		}
		code := hex.EncodeToString(b[:])
		if err := h.F.Store.CreateToken(req.ctx, u.ID, "enrol", forge.HashToken(code), "", time.Now().Add(forge.EnrolmentTokenTTL)); err != nil {
			req.fail(err)
			return
		}
		p := req.page("Enrolment code")
		p.Textf("On the new device, open %s with its certificate, choose \"add this certificate to an existing account\", and enter:", h.F.Config.GeminiURL("/account"))
		p.Pre("code", code)
		p.Textf("The code expires in %s and can be used once.", forge.EnrolmentTokenTTL)
		h.footer(p, req)
		req.send(p)
		return
	}
	if len(rest) == 2 && rest[0] == "revoke" {
		certs, _ := h.F.Store.ListCertificates(req.ctx, u.ID)
		for _, c := range certs {
			if c.SPKISHA256 == rest[1] {
				if c.SPKISHA256 == req.id.SPKI {
					_ = gemini.BadRequest(req.w, "cannot revoke the certificate in use; do it from another device")
					return
				}
				if !strings.EqualFold(strings.TrimSpace(req.Query()), "revoke") {
					_ = gemini.Input(req.w, "Type \"revoke\" to revoke this certificate")
					return
				}
				if err := h.F.Store.RevokeCertificate(req.ctx, c.ID); err != nil {
					req.fail(err)
					return
				}
				_ = gemini.Redirect(req.w, "/account/certs")
				return
			}
		}
		_ = gemini.NotFound(req.w)
		return
	}
	certs, err := h.F.Store.ListCertificates(req.ctx, u.ID)
	if err != nil {
		req.fail(err)
		return
	}
	p := req.page("Certificates")
	for _, c := range certs {
		state := "active"
		if !c.RevokedAt.IsZero() {
			state = "revoked " + date(c.RevokedAt)
		}
		me := ""
		if c.SPKISHA256 == req.id.SPKI {
			me = " (this device)"
		}
		p.Textf("%s%s: %s, added %s, last used %s", c.Label, me, state, date(c.CreatedAt), when(c.LastUsedAt))
		p.Text("public key " + c.SPKISHA256)
		if c.RevokedAt.IsZero() && c.SPKISHA256 != req.id.SPKI {
			p.Link("/account/certs/revoke/"+c.SPKISHA256, "revoke")
		}
		p.Blank()
	}
	p.Link("/account/certs/enrol-code", "generate a code to enrol another device")
	h.footer(p, req)
	req.send(p)
}

// serveTitan dispatches Titan uploads. Every Titan write requires a
// registered identity; the body is read only after authorisation.
func (h *Handler) serveTitan(req *request) {
	u := req.requireUser()
	if u == nil {
		return
	}
	if !req.Titan.Edit && !h.limiter.allow(u.ID, h.F.Config.Limits.WriteRatePerMinute) {
		_ = req.w.Header(gemini.StatusSlowDown, "too many writes; wait a minute and retry")
		return
	}
	segs := req.segs
	switch {
	case len(segs) == 2 && segs[0] == "account" && segs[1] == "keys":
		body, ok := h.readTitanText(req, 64<<10)
		if !ok {
			return
		}
		if _, err := h.F.AddSSHKeys(req.ctx, u, body); err != nil {
			req.fail(err)
			return
		}
		_ = gemini.Redirect(req.w, "/account/keys")
	case len(segs) >= 2 && strings.HasPrefix(segs[0], "~"):
		h.titanRepo(req, u, strings.TrimPrefix(segs[0], "~"), segs[1], segs[2:])
	default:
		_ = gemini.NotFound(req.w)
	}
}

// readTitanText reads a bounded text body, enforcing the MIME allowlist for
// text uploads.
func (h *Handler) readTitanText(req *request, limit int64) (string, bool) {
	if req.Titan.Size > limit {
		_ = req.w.Header(gemini.StatusPermanentFailure, fmt.Sprintf("body too large (limit %d bytes)", limit))
		return "", false
	}
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(req.Titan.MIME, ";", 2)[0]))
	if mt != "text/plain" && mt != "text/gemini" && mt != "text/markdown" {
		_ = req.w.Header(gemini.StatusPermanentFailure, "text/plain or text/gemini expected")
		return "", false
	}
	data, err := io.ReadAll(req.Body)
	if err != nil || int64(len(data)) != req.Titan.Size {
		_ = gemini.BadRequest(req.w, "short body")
		return "", false
	}
	if !isValidText(data) {
		_ = gemini.BadRequest(req.w, "body must be UTF-8 text")
		return "", false
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n"), true
}

func isValidText(b []byte) bool { return !isBinary(b) }

// titanRepo dispatches Titan writes under /~owner/name/....
func (h *Handler) titanRepo(req *request, u *store.User, owner, name string, rest []string) {
	acc, err := h.F.LookupRepo(req.ctx, u, owner, name)
	if err != nil {
		req.fail(err)
		return
	}
	rc := &repoCtx{acc: acc, base: "/~" + owner + "/" + name}
	if len(rest) == 0 {
		_ = gemini.NotFound(req.w)
		return
	}
	if h.forwardIfRemote(req, acc.Repo) {
		return
	}
	switch rest[0] {
	case "issues":
		h.titanIssues(req, u, rc, rest[1:])
	case "changes":
		h.titanChanges(req, u, rc, rest[1:])
	case "releases":
		h.titanReleases(req, u, rc, rest[1:])
	case "settings":
		h.titanSettings(req, u, rc, rest[1:])
	default:
		_ = gemini.NotFound(req.w)
	}
}
