package repl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"as215520.net/forge/internal/store"
)

// Release asset files. The leader streams a file over the control plane
// after checking that a release_assets row names it; replicas reconcile
// their <assets>/<owner>/<repo>/ tree with the replicated rows after every
// metadata pull: missing, short or corrupt files are fetched again, files
// without a row are removed. Paths are only ever built from validated tag
// and asset names, never from anything on disk or in a URL directly.

const (
	headerSHA256 = "X-Forge-SHA256"
	// maxAssetBytes bounds one asset transfer (Content-Length).
	maxAssetBytes = 1 << 30
)

// assetNameRe mirrors internal/forge.assetNameRe; keep the two in sync.
var assetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// validTag mirrors the tag rule of forge.CreateRelease (no space, slash or
// backslash, at most 128 bytes) plus what a path segment must never be.
func validTag(tag string) bool {
	return tag != "" && len(tag) <= 128 && tag != "." && tag != ".." && !strings.ContainsAny(tag, " /\\\x00")
}

// validAsset reports whether tag and name are safe path segments.
func validAsset(tag, name string) bool {
	return validTag(tag) && assetNameRe.MatchString(name) && name != "." && name != ".."
}

// handleAsset serves GET /v1/repos/{id}/assets/{tag}/{name} on the leader.
func (n *Node) handleAsset(w http.ResponseWriter, r *http.Request, _ string) {
	rp, ok := n.repoFromRequest(w, r)
	if !ok {
		return
	}
	if rp.LeaderNode != n.opts.Name {
		http.Error(w, "not the leader of this repository; leader is "+rp.LeaderNode, http.StatusConflict)
		return
	}
	tag, name := r.PathValue("tag"), r.PathValue("name")
	if !validAsset(tag, name) {
		http.NotFound(w, r)
		return
	}
	a, err := n.opts.Store.RepoAssetByTagName(r.Context(), rp.ID, tag, name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		n.serverError(w, err)
		return
	}
	f, err := os.Open(n.AssetPath(rp, a.Tag, a.Name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "asset file missing", http.StatusNotFound)
			return
		}
		n.serverError(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.Error(w, "asset file missing", http.StatusNotFound)
		return
	}
	if fi.Size() != a.Size {
		n.log.Error("asset file size differs from its record", "repo", rp.Owner+"/"+rp.Name, "tag", a.Tag, "name", a.Name, "file", fi.Size(), "record", a.Size)
		http.Error(w, "asset file inconsistent", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set(headerSHA256, a.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

// syncAssets reconciles the local asset files of rp with its replicated
// release_assets rows, fetching from peer (the leader). With verify set
// every present file is hashed; otherwise only existence and size are
// checked. Files under the repository's asset directory that no row names
// are removed. Downloads run one at a time. The returned error joins every
// per-file failure; the other files are still processed.
func (n *Node) syncAssets(ctx context.Context, peer string, rp *store.Repo, verify bool) error {
	assets, err := n.opts.Store.RepoAssets(ctx, rp.ID)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(assets))
	var errs []error
	for _, a := range assets {
		if !validAsset(a.Tag, a.Name) {
			errs = append(errs, fmt.Errorf("%s/%s: unsafe tag or asset name in record", a.Tag, a.Name))
			continue
		}
		wanted[a.Tag+"/"+a.Name] = true
		path := n.AssetPath(rp, a.Tag, a.Name)
		ok, err := n.assetMatches(path, a, verify)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", a.Tag, a.Name, err))
			continue
		}
		if ok {
			continue
		}
		if err := n.fetchAsset(ctx, peer, rp, a, path); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", a.Tag, a.Name, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		n.log.Info("asset replicated", "repo", rp.Owner+"/"+rp.Name, "tag", a.Tag, "name", a.Name, "size", a.Size)
	}
	if ctx.Err() == nil {
		if err := n.pruneAssets(rp, wanted); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// assetMatches reports whether the file at path is the asset described by
// a: a regular file of the recorded size and, when verify is set, the
// recorded sha256. A missing file is simply "no match".
func (n *Node) assetMatches(path string, a store.RepoAsset, verify bool) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != a.Size {
		return false, nil
	}
	if !verify {
		return true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.SHA256), nil
}

// assetTmpDir is where downloads land before the rename into place: the
// data directory's tmp/, a sibling of the assets tree on the same
// filesystem (forge's Config.TmpDir).
func (n *Node) assetTmpDir() string {
	return filepath.Join(filepath.Dir(filepath.Clean(n.opts.AssetsDir)), "tmp")
}

// fetchAsset downloads one asset from peer into a temporary file, verifies
// its length and sha256 against the record, and renames it to path.
func (n *Node) fetchAsset(ctx context.Context, peer string, rp *store.Repo, a store.RepoAsset, path string) error {
	if a.Size <= 0 || a.Size > maxAssetBytes {
		return fmt.Errorf("recorded size %d outside 1..%d", a.Size, maxAssetBytes)
	}
	// Bound a stalled transfer: a generous floor plus 1 MiB/s.
	ctx, cancel := context.WithTimeout(ctx, time.Minute+time.Duration(a.Size>>20)*time.Second)
	defer cancel()
	req, err := n.newRequest(ctx, peer, http.MethodGet, "/v1/repos/"+strconv.FormatInt(rp.ID, 10)+"/assets/"+a.Tag+"/"+a.Name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := n.assetClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: GET %s: %s: %s", ErrRemote, req.URL.Path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if resp.ContentLength < 0 {
		return errors.New("leader sent no Content-Length")
	}
	if resp.ContentLength != a.Size {
		return fmt.Errorf("leader offers %d bytes, record says %d", resp.ContentLength, a.Size)
	}
	if got := resp.Header.Get(headerSHA256); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("leader offers sha256 %q, record says %q", got, a.SHA256)
	}
	if err := os.MkdirAll(n.assetTmpDir(), 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(n.assetTmpDir(), "repl-asset-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	got, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if got != a.Size {
		_ = tmp.Close()
		return fmt.Errorf("received %d bytes, expected %d", got, a.Size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(sum, a.SHA256) {
		_ = tmp.Close()
		return fmt.Errorf("sha256 mismatch: received %s, record says %s", sum, a.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// pruneAssets removes files under <assets>/<owner>/<repo>/ that no
// release_assets row names, and the tag directories left empty. It only
// descends the two fixed levels of the layout and never follows symlinks;
// entries that do not fit the layout are left alone.
func (n *Node) pruneAssets(rp *store.Repo, wanted map[string]bool) error {
	root := filepath.Join(n.opts.AssetsDir, rp.Owner, rp.Name)
	tags, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, td := range tags {
		if !td.IsDir() || !validTag(td.Name()) {
			continue
		}
		dir := filepath.Join(root, td.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		remaining := 0
		for _, fe := range files {
			if fe.IsDir() {
				remaining++
				continue
			}
			if wanted[td.Name()+"/"+fe.Name()] {
				remaining++
				continue
			}
			if err := os.Remove(filepath.Join(dir, fe.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
				remaining++
				continue
			}
			n.log.Info("stale asset removed", "repo", rp.Owner+"/"+rp.Name, "tag", td.Name(), "name", fe.Name())
		}
		if remaining == 0 {
			_ = os.Remove(dir)
		}
	}
	return errors.Join(errs...)
}
