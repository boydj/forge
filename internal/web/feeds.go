package web

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"as215520.net/forge/internal/gemini"
	"as215520.net/forge/internal/store"
)

// Gemfeed conventions (gemini://geminiprotocol.net/docs/companion/subscription.gmi):
// first heading is the feed title, an optional second-level heading is the
// subtitle, and entries are link lines "=> url YYYY-MM-DD - title".
// Atom is served alongside because some clients (Amfora) only read Atom.

const feedLimit = 60

func (h *Handler) feedAll(req *request, atom bool) {
	events, err := h.F.Store.Events(req.ctx, store.EventQuery{Viewer: req.id.UserID(), Limit: feedLimit})
	if err != nil {
		req.fail(err)
		return
	}
	h.writeFeed(req, h.F.Config.Title+" activity", "/", events, atom)
}

func (h *Handler) feedUser(req *request, name string, atom bool) {
	u, err := h.F.Store.UserByName(req.ctx, name)
	if err != nil {
		req.fail(err)
		return
	}
	events, err := h.F.Store.Events(req.ctx, store.EventQuery{Viewer: req.id.UserID(), UserID: u.ID, Limit: feedLimit})
	if err != nil {
		req.fail(err)
		return
	}
	h.writeFeed(req, u.Name+" activity", "/~"+u.Name+"/", events, atom)
}

func (h *Handler) feedRepo(req *request, rc *repoCtx, kinds []string, atom bool) {
	r := rc.acc.Repo
	events, err := h.F.Store.Events(req.ctx, store.EventQuery{Viewer: req.id.UserID(), RepoID: r.ID, Kinds: kinds, Limit: feedLimit})
	if err != nil {
		req.fail(err)
		return
	}
	title := r.Owner + "/" + r.Name + " activity"
	if len(kinds) > 0 {
		title = r.Owner + "/" + r.Name + " " + strings.SplitN(kinds[0], ".", 2)[0] + "s"
	}
	h.writeFeed(req, title, rc.base+"/", events, atom)
}

func (h *Handler) writeFeed(req *request, title, home string, events []*store.Event, atom bool) {
	if atom {
		h.writeAtom(req, title, home, events)
		return
	}
	p := req.page(title)
	p.Heading(2, "Gemfeed from "+h.F.Config.Hostname)
	p.Link(home, "home")
	p.Blank()
	for _, e := range events {
		// The subject must not start with a digit right after the date, and
		// a title is required, for Lagrange's feed parser.
		p.Link(e.Path, date(e.CreatedAt)+" - "+e.Subject)
	}
	if len(events) == 0 {
		p.Text("No activity yet.")
	}
	req.send(p)
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	NS      string      `xml:"xmlns,attr"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    atomLink    `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Link    atomLink `xml:"link"`
	Author  *struct {
		Name string `xml:"name"`
	} `xml:"author,omitempty"`
	Summary string `xml:"summary,omitempty"`
}

func (h *Handler) writeAtom(req *request, title, home string, events []*store.Event) {
	f := atomFeed{NS: "http://www.w3.org/2005/Atom", Title: title, ID: h.F.Config.GeminiURL(home), Link: atomLink{Href: h.F.Config.GeminiURL(home)}}
	f.Updated = time.Now().UTC().Format(time.RFC3339)
	if len(events) > 0 {
		f.Updated = events[0].CreatedAt.UTC().Format(time.RFC3339)
	}
	for _, e := range events {
		en := atomEntry{
			Title:   e.Subject,
			ID:      fmt.Sprintf("%s#event-%d", h.F.Config.GeminiURL(e.Path), e.ID),
			Updated: e.CreatedAt.UTC().Format(time.RFC3339),
			Link:    atomLink{Href: h.F.Config.GeminiURL(e.Path)},
		}
		if e.UserName != "" {
			en.Author = &struct {
				Name string `xml:"name"`
			}{Name: e.UserName}
		}
		f.Entries = append(f.Entries, en)
	}
	_ = req.w.Header(gemini.StatusSuccess, "application/atom+xml; charset=utf-8")
	_, _ = req.w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(req.w)
	enc.Indent("", "  ")
	_ = enc.Encode(f)
	_, _ = req.w.Write([]byte("\n"))
}
