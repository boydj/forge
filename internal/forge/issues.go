package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"as215520.net/forge/internal/store"
)

// Text limits for tracker content.
const (
	MaxTitleLen = 200
)

// SplitTitleBody splits a Titan body into a title (first non-empty line)
// and the remaining text.
func SplitTitleBody(text string) (string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return "", ""
	}
	title := strings.TrimSpace(strings.TrimLeft(lines[i], "# "))
	body := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
	return title, body
}

// checkText validates user text against limits and encoding.
func (f *Forge) checkText(title, body string) error {
	if title != "" && (utf8.RuneCountInString(title) > MaxTitleLen || strings.ContainsAny(title, "\r\n")) {
		return ErrTooLarge
	}
	if int64(len(body)) > f.Config.Limits.MaxTextBytes {
		return ErrTooLarge
	}
	if !utf8.ValidString(title) || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return ErrNotAcceptable
	}
	return nil
}

func issuePath(r *store.Repo, n int64) string {
	return fmt.Sprintf("/~%s/%s/issues/%d", r.Owner, r.Name, n)
}

// OpenIssue creates an issue. Any reader of the repository may open one.
func (f *Forge) OpenIssue(ctx context.Context, u *store.User, acc Access, text string) (*store.Issue, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if !acc.CanRead() {
		return nil, ErrNotFound
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	title, body := SplitTitleBody(text)
	if title == "" {
		return nil, ErrNotAcceptable
	}
	if err := f.checkText(title, body); err != nil {
		return nil, err
	}
	is, err := f.Store.CreateIssue(ctx, acc.Repo.ID, u.ID, title, body)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"number": is.Number})
	f.Event(ctx, store.EventIssueOpen, acc.Repo, u, fmt.Sprintf("%s opened issue #%d: %s", u.Name, is.Number, is.Title), issuePath(acc.Repo, is.Number), payload)
	return is, nil
}

// canManageIssue reports whether u may edit/close the issue.
func canManageIssue(u *store.User, acc Access, is *store.Issue) bool {
	return u != nil && (acc.CanWrite() || is.AuthorID == u.ID)
}

// EditIssue replaces title and body.
func (f *Forge) EditIssue(ctx context.Context, u *store.User, acc Access, is *store.Issue, text string) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !canManageIssue(u, acc, is) {
		return ErrForbidden
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	title, body := SplitTitleBody(text)
	if title == "" {
		return ErrNotAcceptable
	}
	if err := f.checkText(title, body); err != nil {
		return err
	}
	if err := f.Store.UpdateIssue(ctx, is.ID, title, body); err != nil {
		return err
	}
	f.Event(ctx, store.EventIssueEdit, acc.Repo, u, fmt.Sprintf("%s edited issue #%d: %s", u.Name, is.Number, title), issuePath(acc.Repo, is.Number), nil)
	return nil
}

// SetIssueState closes or reopens an issue.
func (f *Forge) SetIssueState(ctx context.Context, u *store.User, acc Access, is *store.Issue, closed bool) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !canManageIssue(u, acc, is) {
		return ErrForbidden
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	state, kind, verb := "open", store.EventIssueReopen, "reopened"
	if closed {
		state, kind, verb = "closed", store.EventIssueClose, "closed"
	}
	if is.State == state {
		return nil
	}
	if err := f.Store.SetIssueState(ctx, is.ID, state); err != nil {
		return err
	}
	f.Event(ctx, kind, acc.Repo, u, fmt.Sprintf("%s %s issue #%d: %s", u.Name, verb, is.Number, is.Title), issuePath(acc.Repo, is.Number), nil)
	return nil
}

// Comment adds a comment to an issue or change.
func (f *Forge) Comment(ctx context.Context, u *store.User, acc Access, kind string, targetID, number int64, title, text string) (*store.Comment, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if !acc.CanRead() {
		return nil, ErrNotFound
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if body == "" {
		return nil, ErrNotAcceptable
	}
	if err := f.checkText("", body); err != nil {
		return nil, err
	}
	c, err := f.Store.AddComment(ctx, acc.Repo.ID, kind, targetID, u.ID, body)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/~%s/%s/%ss/%d", acc.Repo.Owner, acc.Repo.Name, kind, number)
	f.Event(ctx, store.EventComment, acc.Repo, u, fmt.Sprintf("%s commented on %s #%d: %s", u.Name, kind, number, title), path, nil)
	return c, nil
}

// EditComment replaces a comment body (author or repository writer).
func (f *Forge) EditComment(ctx context.Context, u *store.User, acc Access, c *store.Comment, text string) error {
	if u == nil {
		return ErrAuthRequired
	}
	if c.AuthorID != u.ID && !acc.CanWrite() {
		return ErrForbidden
	}
	body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if body == "" {
		return ErrNotAcceptable
	}
	if err := f.checkText("", body); err != nil {
		return err
	}
	return f.Store.UpdateComment(ctx, c.ID, body)
}

// DeleteComment hides a comment (author or repository writer).
func (f *Forge) DeleteComment(ctx context.Context, u *store.User, acc Access, c *store.Comment) error {
	if u == nil {
		return ErrAuthRequired
	}
	if c.AuthorID != u.ID && !acc.CanWrite() {
		return ErrForbidden
	}
	return f.Store.DeleteComment(ctx, c.ID)
}

// LookupIssue loads an issue by number for an access context.
func (f *Forge) LookupIssue(ctx context.Context, acc Access, number int64) (*store.Issue, error) {
	is, err := f.Store.IssueByNumber(ctx, acc.Repo.ID, number)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return is, err
}
