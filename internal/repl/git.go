package repl

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"as215520.net/forge/internal/store"
	gitvcs "as215520.net/forge/internal/vcs/git"
)

// Git transfer. The leader serves git's smart-HTTP read protocol on the
// control plane by spawning `git upload-pack --stateless-rpc` exactly as
// git-http-backend does; replicas run an ordinary `git fetch` against it with
// the cluster secret in an extra header. Only upload-pack is offered: pushes
// go through SSH on the leader (ADR 0011).

const (
	uploadPack = "git-upload-pack"
	gitService = "upload-pack"
)

// pktLine encodes one pkt-line.
func pktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}

// gitRepoFromRequest resolves {owner}/{repo}.git to a led, non-deleted git
// repository on disk.
func (n *Node) gitRepoFromRequest(w http.ResponseWriter, r *http.Request) (*store.Repo, string, bool) {
	owner, name := r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git")
	if owner == "" || name == "" || strings.ContainsAny(owner+name, "/\\") || strings.HasPrefix(owner, ".") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return nil, "", false
	}
	rp, err := n.opts.Store.RepoByPath(r.Context(), owner, name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, "", false
	}
	if err != nil {
		n.serverError(w, err)
		return nil, "", false
	}
	if rp.VCS != "git" {
		http.Error(w, "not a git repository", http.StatusNotFound)
		return nil, "", false
	}
	if rp.LeaderNode != n.opts.Name {
		http.Error(w, "not the leader of this repository; leader is "+rp.LeaderNode, http.StatusConflict)
		return nil, "", false
	}
	path := n.RepoPath(rp.Owner, rp.Name)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		http.Error(w, "repository files missing", http.StatusNotFound)
		return nil, "", false
	}
	return rp, path, true
}

// gitEnv returns the subprocess environment with the client's protocol
// request (Git-Protocol header -> GIT_PROTOCOL) applied.
func gitEnv(base []string, r *http.Request) []string {
	if v := r.Header.Get("Git-Protocol"); v != "" && !strings.ContainsAny(v, "\x00\n") {
		return append(base, "GIT_PROTOCOL="+v)
	}
	return base
}

// handleInfoRefs serves GET .../info/refs?service=git-upload-pack.
func (n *Node) handleInfoRefs(w http.ResponseWriter, r *http.Request, _ string) {
	if r.URL.Query().Get("service") != uploadPack {
		http.Error(w, "only git-upload-pack is served", http.StatusForbidden)
		return
	}
	_, path, ok := n.gitRepoFromRequest(w, r)
	if !ok {
		return
	}
	release, err := n.opts.Git.Acquire(r.Context())
	if err != nil {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	cmd := n.opts.Git.Command(r.Context(), "", gitService, "--stateless-rpc", "--advertise-refs", path)
	cmd.Env = gitEnv(cmd.Env, r)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		n.log.Error("upload-pack advertise failed", "path", path, "err", err, "stderr", strings.TrimSpace(stderr.String()))
		http.Error(w, "upload-pack failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+uploadPack+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, pktLine("# service="+uploadPack+"\n"))
	_, _ = io.WriteString(w, "0000")
	_, _ = w.Write(out)
}

// handleUploadPack serves POST .../git-upload-pack.
func (n *Node) handleUploadPack(w http.ResponseWriter, r *http.Request, _ string) {
	_, path, ok := n.gitRepoFromRequest(w, r)
	if !ok {
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "bad gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	release, err := n.opts.Git.Acquire(r.Context())
	if err != nil {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	cmd := n.opts.Git.Command(r.Context(), "", gitService, "--stateless-rpc", path)
	cmd.Env = gitEnv(cmd.Env, r)
	cmd.Stdin = body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	w.Header().Set("Content-Type", "application/x-"+uploadPack+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	cmd.Stdout = &flushWriter{w: w}
	if err := cmd.Run(); err != nil {
		// The status line is already sent once output started; log only.
		n.log.Error("upload-pack failed", "path", path, "err", err, "stderr", strings.TrimSpace(stderr.String()))
	}
}

type flushWriter struct{ w http.ResponseWriter }

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// withGitConfig appends configuration entries to a hardened git environment
// produced by Backend.Env. It rewrites GIT_CONFIG_COUNT; os/exec uses the
// last value of a duplicated key, so appending is sufficient. The secret
// travels in the environment rather than on the command line so it does not
// show in process listings.
func withGitConfig(env []string, kv ...[2]string) []string {
	count := 0
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			count, _ = strconv.Atoi(v)
		}
	}
	for _, p := range kv {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", count, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, p[1]))
		count++
	}
	return append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

// gitURL is the control-plane clone URL of a repository on a peer.
func gitURL(addr, owner, name string) string {
	return "http://" + addr + "/v1/git/" + owner + "/" + name + ".git"
}

// fetchRepo mirrors branches and tags of rp from its leader into the local
// bare repository, creating it when missing. It is idempotent: an
// interrupted fetch leaves refs untouched (git updates them only after the
// pack is verified) and the next call resumes.
func (n *Node) fetchRepo(ctx context.Context, rp *store.Repo, leaderAddr string) error {
	path := n.RepoPath(rp.Owner, rp.Name)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		branch := rp.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		if err := n.opts.Git.Init(ctx, path, branch); err != nil {
			return fmt.Errorf("init replica: %w", err)
		}
	}
	release, err := n.opts.Git.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	cmd := n.opts.Git.Command(ctx, path, "fetch", "--quiet", "--prune", "--no-tags", "--no-write-fetch-head",
		"--", gitURL(leaderAddr, rp.Owner, rp.Name), "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	cmd.Env = withGitConfig(cmd.Env,
		[2]string{"protocol.http.allow", "always"},
		[2]string{"http.followRedirects", "false"},
		[2]string{"http.extraHeader", "Authorization: Bearer " + string(n.secret)},
		[2]string{"http.extraHeader", headerNode + ": " + n.opts.Name},
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("fetch interrupted: %w", ctx.Err())
		}
		return &gitvcs.Error{Args: []string{"fetch"}, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	if rp.DefaultBranch != "" {
		if repo, err := n.opts.Git.Open(path); err == nil {
			if cur, err := repo.DefaultBranch(ctx); err == nil && cur != rp.DefaultBranch {
				if err := n.opts.Git.SetDefaultBranch(ctx, path, rp.DefaultBranch); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
