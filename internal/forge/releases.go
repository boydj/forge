package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"as215520.net/forge/internal/store"
)

var assetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// assetMIMEAllowed lists MIME types accepted for release assets. Anything
// else is stored as application/octet-stream to avoid MIME confusion.
var assetMIMEAllowed = map[string]bool{
	"application/gzip": true, "application/x-gzip": true, "application/zip": true, "application/x-tar": true,
	"application/x-xz": true, "application/zstd": true, "application/x-bzip2": true, "application/x-7z-compressed": true,
	"application/octet-stream": true, "application/pgp-signature": true, "application/pdf": true,
	"text/plain": true, "text/gemini": true, "text/markdown": true, "text/x-patch": true, "text/x-diff": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/svg+xml": false,
	"audio/ogg": true, "audio/flac": true, "video/webm": true,
}

func releasePath(r *store.Repo, tag string) string {
	return fmt.Sprintf("/~%s/%s/releases/%s", r.Owner, r.Name, tag)
}

// AssetPath is the on-disk location of a release asset.
func (f *Forge) AssetPath(r *store.Repo, tag, name string) string {
	return filepath.Join(f.Config.AssetsDir(), r.Owner, r.Name, tag, name)
}

// CreateRelease publishes release notes for an existing tag. Body format:
// first line tag, second line title (optional; defaults to tag), rest notes.
func (f *Forge) CreateRelease(ctx context.Context, u *store.User, acc Access, text string) (*store.Release, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if !acc.CanWrite() {
		return nil, ErrForbidden
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	tag, rest := SplitTitleBody(text)
	title, body := SplitTitleBody(rest)
	if title == "" {
		title = tag
	}
	if tag == "" || strings.ContainsAny(tag, " /\\") || len(tag) > 128 {
		return nil, ErrNotAcceptable
	}
	if err := f.checkText(title, body); err != nil {
		return nil, err
	}
	repo, err := f.Open(acc.Repo)
	if err != nil {
		return nil, err
	}
	if _, err := repo.Resolve(ctx, "refs/tags/"+tag); err != nil {
		if _, err := repo.Resolve(ctx, tag); err != nil {
			return nil, fmt.Errorf("tag %s does not exist in the repository: %w", tag, ErrNotFound)
		}
	}
	rel, err := f.Store.CreateRelease(ctx, acc.Repo.ID, u.ID, tag, title, body)
	if errors.Is(err, store.ErrConflict) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"tag": tag})
	f.Event(ctx, store.EventRelease, acc.Repo, u, fmt.Sprintf("%s released %s/%s %s: %s", u.Name, acc.Repo.Owner, acc.Repo.Name, tag, title), releasePath(acc.Repo, tag), payload)
	return rel, nil
}

// EditRelease replaces title and notes.
func (f *Forge) EditRelease(ctx context.Context, u *store.User, acc Access, rel *store.Release, text string) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !acc.CanWrite() {
		return ErrForbidden
	}
	if acc.Repo.Archived {
		return ErrArchived
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
	return f.Store.UpdateRelease(ctx, rel.ID, title, body)
}

// DeleteRelease removes a release and its assets.
func (f *Forge) DeleteRelease(ctx context.Context, u *store.User, acc Access, rel *store.Release) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !acc.CanWrite() {
		return ErrForbidden
	}
	if acc.Repo.Archived {
		return ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	if err := f.Store.DeleteRelease(ctx, rel.ID); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Dir(f.AssetPath(acc.Repo, rel.Tag, "x")))
	return nil
}

// AddAsset stores an uploaded asset. The body is streamed to a temporary
// file on the data filesystem, hashed, then renamed into place.
func (f *Forge) AddAsset(ctx context.Context, u *store.User, acc Access, rel *store.Release, name, mimeType string, size int64, body io.Reader) (*store.ReleaseAsset, error) {
	if u == nil {
		return nil, ErrAuthRequired
	}
	if !acc.CanWrite() {
		return nil, ErrForbidden
	}
	if acc.Repo.Archived {
		return nil, ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return nil, ErrNotLeader
	}
	if !assetNameRe.MatchString(name) || name == "." || name == ".." {
		return nil, ErrInvalidName
	}
	if size <= 0 || size > f.Config.Limits.MaxAssetBytes {
		return nil, ErrTooLarge
	}
	if len(rel.Assets) >= 32 {
		return nil, ErrQuota
	}
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if !assetMIMEAllowed[mt] {
		mt = "application/octet-stream"
	}
	if !u.Admin {
		used, err := f.Store.AssetBytesByOwner(ctx, acc.Repo.OwnerID)
		if err != nil {
			return nil, err
		}
		_, repoBytes, err := f.Store.CountReposByOwner(ctx, acc.Repo.OwnerID)
		if err != nil {
			return nil, err
		}
		if used+repoBytes+size > f.Config.Limits.MaxUserBytes {
			return nil, ErrQuota
		}
	}
	if err := f.CheckDisk(size); err != nil {
		return nil, err
	}
	dst := f.AssetPath(acc.Repo, rel.Tag, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(f.Config.TmpDir(), "asset-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, size))
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if n != size {
		_ = tmp.Close()
		return nil, ErrNotAcceptable
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return nil, err
	}
	a, err := f.Store.AddReleaseAsset(ctx, &store.ReleaseAsset{ReleaseID: rel.ID, Name: name, Size: size, MIME: mt, SHA256: hex.EncodeToString(h.Sum(nil))})
	if err != nil {
		_ = os.Remove(dst)
		return nil, err
	}
	f.Event(ctx, store.EventReleaseAsset, acc.Repo, u, fmt.Sprintf("%s attached %s to %s/%s %s", u.Name, name, acc.Repo.Owner, acc.Repo.Name, rel.Tag), releasePath(acc.Repo, rel.Tag), nil)
	return a, nil
}

// RemoveAsset deletes an asset.
func (f *Forge) RemoveAsset(ctx context.Context, u *store.User, acc Access, rel *store.Release, a *store.ReleaseAsset) error {
	if u == nil {
		return ErrAuthRequired
	}
	if !acc.CanWrite() {
		return ErrForbidden
	}
	if acc.Repo.Archived {
		return ErrArchived
	}
	if !f.IsLeader(acc.Repo) {
		return ErrNotLeader
	}
	if err := f.Store.DeleteReleaseAsset(ctx, a.ID); err != nil {
		return err
	}
	return os.Remove(f.AssetPath(acc.Repo, rel.Tag, a.Name))
}

// OpenAsset opens an asset file for reading.
func (f *Forge) OpenAsset(acc Access, rel *store.Release, a *store.ReleaseAsset) (*os.File, error) {
	if !assetNameRe.MatchString(a.Name) {
		return nil, ErrNotFound
	}
	return os.Open(f.AssetPath(acc.Repo, rel.Tag, a.Name))
}

// LookupRelease loads a release by tag.
func (f *Forge) LookupRelease(ctx context.Context, acc Access, tag string) (*store.Release, error) {
	rel, err := f.Store.ReleaseByTag(ctx, acc.Repo.ID, tag)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return rel, err
}
