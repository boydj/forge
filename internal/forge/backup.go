package forge

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/vcs"
)

// Backup writes a consistent backup. When out ends in .db only a database
// snapshot (VACUUM INTO) is written; otherwise a gzip-compressed tar with
// the snapshot at forge.db, every bare repository under repos/ (mirrored
// with git bundle so refs and objects are consistent), release assets under
// assets/ and the TLS/SSH identities under tls/ and ssh/.
func (f *Forge) Backup(ctx context.Context, out string) error {
	if strings.HasSuffix(out, ".db") {
		_ = os.Remove(out)
		return f.Store.Backup(ctx, out)
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	fail := func(err error) error { _ = tw.Close(); _ = gz.Close(); _ = tmp.Close(); return err }

	snap := filepath.Join(f.Config.TmpDir(), fmt.Sprintf("snapshot-%d.db", time.Now().UnixNano()))
	if err := f.Store.Backup(ctx, snap); err != nil {
		return fail(err)
	}
	defer os.Remove(snap)
	if err := addFile(tw, snap, "forge.db"); err != nil {
		return fail(err)
	}
	repos, err := f.Store.AllRepos(ctx)
	if err != nil {
		return fail(err)
	}
	for _, r := range repos {
		repo, err := f.Open(r)
		if err != nil {
			f.Log.Warn("backup: skipping repository", "repo", r.Owner+"/"+r.Name, "err", err)
			continue
		}
		if empty, _ := repo.Empty(ctx); empty {
			continue
		}
		bundle := filepath.Join(f.Config.TmpDir(), fmt.Sprintf("bundle-%d.bundle", r.ID))
		cmd := f.Git.Command(ctx, repo.Path(), "bundle", "create", "--quiet", bundle, "--all")
		if outb, err := cmd.CombinedOutput(); err != nil {
			return fail(fmt.Errorf("bundle %s/%s: %v: %s", r.Owner, r.Name, err, outb))
		}
		err = addFile(tw, bundle, fmt.Sprintf("repos/%s/%s.bundle", r.Owner, r.Name))
		_ = os.Remove(bundle)
		if err != nil {
			return fail(err)
		}
	}
	for _, sub := range []string{"assets", "tls", "ssh"} {
		dir := filepath.Join(f.Config.DataDir, sub)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(f.Config.DataDir, p)
			return addFile(tw, p, filepath.ToSlash(rel))
		})
		if err != nil {
			return fail(err)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}

func addFile(tw *tar.Writer, path, name string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, fh)
	return err
}

// Restore recreates data from a tar backup into DataDir. The daemon must be
// stopped. Existing repositories with the same name are replaced.
func (f *Forge) Restore(ctx context.Context, in string) error {
	fh, err := os.Open(in)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	// Close the live database first: closing later would checkpoint the old
	// WAL over the restored file.
	_ = f.Store.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(f.Config.DBPath() + suffix)
	}
	var bundles []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) || hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsafe entry %q", hdr.Name)
		}
		var dst string
		switch {
		case name == "forge.db":
			dst = f.Config.DBPath()
			for _, suffix := range []string{"-wal", "-shm"} {
				_ = os.Remove(dst + suffix)
			}
		case strings.HasPrefix(name, "repos/") && strings.HasSuffix(name, ".bundle"):
			dst = filepath.Join(f.Config.TmpDir(), "restore-"+strings.ReplaceAll(strings.TrimPrefix(name, "repos/"), "/", "__"))
			bundles = append(bundles, dst)
		default:
			dst = filepath.Join(f.Config.DataDir, name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return err
		}
		_ = out.Close()
	}
	// Reopen the restored database and recreate repositories from bundles.
	st, err := store.Open(ctx, f.Config.DBPath(), f.Config.Node)
	if err != nil {
		return err
	}
	f.Store = st
	repos, err := st.AllRepos(ctx)
	if err != nil {
		return err
	}
	byName := map[string]*store.Repo{}
	for _, r := range repos {
		byName[r.Owner+"__"+r.Name+".bundle"] = r
	}
	for _, b := range bundles {
		key := strings.TrimPrefix(filepath.Base(b), "restore-")
		r, ok := byName[key]
		if !ok {
			f.Log.Warn("restore: bundle without repository record", "bundle", key)
			continue
		}
		path := f.RepoPath(r.Owner, r.Name)
		_ = os.RemoveAll(path)
		if err := f.Git.Init(ctx, path, r.DefaultBranch); err != nil {
			return err
		}
		cmd := f.Git.Command(ctx, path, "fetch", "--quiet", "--", b, "+refs/*:refs/*")
		if outb, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("restore %s/%s: %v: %s", r.Owner, r.Name, err, outb)
		}
		_ = os.Remove(b)
	}
	// Repositories without bundles (empty at backup time) get fresh directories.
	for _, r := range repos {
		if _, err := os.Stat(f.RepoPath(r.Owner, r.Name)); os.IsNotExist(err) {
			_ = f.Git.Init(ctx, f.RepoPath(r.Owner, r.Name), r.DefaultBranch)
		}
	}
	return nil
}

// ChangeRefRetention is how long refs of closed changes are kept.
const ChangeRefRetention = 90 * 24 * time.Hour

// Maintain runs housekeeping: purge deleted repositories and tokens, git
// gc on every repository, size refresh, and database optimisation. With
// check set it also runs fsck and reports corruption.
func (f *Forge) Maintain(ctx context.Context, check bool) (map[string]int, error) {
	stats := map[string]int{}
	n, err := f.PurgeDeleted(ctx)
	if err != nil {
		return stats, err
	}
	stats["purged"] = n
	if err := f.Store.PurgeTokens(ctx); err != nil {
		return stats, err
	}
	repos, err := f.Store.AllRepos(ctx)
	if err != nil {
		return stats, err
	}
	// Refs of changes closed more than ChangeRefRetention ago are dropped so
	// abandoned proposals stop consuming the owner's quota (ADR 0012).
	if stale, err := f.Store.StaleClosedChanges(ctx, time.Now().Add(-ChangeRefRetention)); err == nil {
		for _, ch := range stale {
			r, err := f.Store.RepoByID(ctx, ch.RepoID)
			if err != nil {
				continue
			}
			repo, err := f.Open(r)
			if err != nil {
				continue
			}
			refs, err := repo.RefsMatching(ctx, fmt.Sprintf("refs/changes/%d/", ch.Number))
			if err != nil || len(refs) == 0 {
				continue
			}
			var dels []vcs.RefUpdate
			for _, ref := range refs {
				dels = append(dels, vcs.RefUpdate{Ref: ref.Name, New: "", Old: ref.Target})
			}
			if err := repo.UpdateRefs(ctx, dels, "forge: prune closed change"); err == nil {
				stats["pruned_changes"]++
			}
		}
	}
	for _, r := range repos {
		repo, err := f.Open(r)
		if err != nil {
			stats["missing"]++
			continue
		}
		// Repos created before the sharedRepository fix carry a setgid-bit
		// config that RestrictSUIDSGID blocks on push; unset it (best effort).
		_, _ = f.Git.Command(ctx, repo.Path(), "config", "--unset", "core.sharedRepository").Output()
		cmd := f.Git.Command(ctx, repo.Path(), "-c", "gc.auto=6700", "-c", "gc.reflogExpireUnreachable=now", "gc", "--auto", "--quiet")
		if outb, err := cmd.CombinedOutput(); err != nil {
			f.Log.Warn("gc failed", "repo", r.Owner+"/"+r.Name, "err", err, "out", string(outb))
			stats["gc_failed"]++
		}
		if check {
			if err := repo.Check(ctx); err != nil {
				f.Log.Error("fsck failed", "repo", r.Owner+"/"+r.Name, "err", err)
				stats["corrupt"]++
			}
		}
		if _, err := f.RefreshSize(ctx, r); err == nil {
			stats["sized"]++
		}
	}
	if _, err := f.Store.DB().ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return stats, err
	}
	if _, err := f.Store.DB().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return stats, err
	}
	return stats, nil
}
