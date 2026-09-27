package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/fsx"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

func init() {
	meta.BeforeJoin = protectExisting
	meta.BeforeShare = protectExisting
}

// snapshotDir holds pre-sync snapshots when no backup folder is available.
func snapshotDir() string { return filepath.Join(paths.Root(paths.Local), "Syncer", "snapshots") }

// protectExisting saves the files already in path as a restore point before
// this PC starts exchanging them with other PCs. If the other PC's copy wins,
// this PC's saves can still be restored ("As it was before <time>").
func protectExisting(ctx context.Context, id, label, path string) error {
	// Mod folders are too big to copy to Drive and can be downloaded again;
	// files replaced by another PC's are kept by Syncthing's versioning (and
	// deployed mods get their own snapshot before every update).
	if isMod(store.LoadSettings(), id) {
		return nil
	}
	key := id + "|" + strings.ToLower(filepath.Clean(path))
	// Adding the folder can fail after the snapshot (e.g. a timeout); don't
	// copy everything again on the retry.
	if t, ok := store.LoadState().Protected[key]; ok && time.Since(t) < 24*time.Hour {
		return nil
	}
	target, ok := backupTarget(store.LoadSettings())
	if !ok {
		target = snapshotDir()
	}
	// Big save folders take a while; don't let a short UI timeout cut it off.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	defer cancel()
	exclude := store.LoadSettings().Exclude[dismissKey(path)]
	n, err := backup.Snapshot(ctx, target, backup.Folder{ID: id, Label: label, Path: path, Exclude: exclude})
	if err != nil {
		return fmt.Errorf("could not save the existing files of %s before syncing, will retry: %w", label, err)
	}
	if n > 0 {
		logx.Printf("saved %d existing file(s) of %s as a restore point before syncing → %s", n, label, target)
	}
	store.UpdateState(func(st *store.State) {
		if st.Protected == nil {
			st.Protected = map[string]time.Time{}
		}
		for k, t := range st.Protected {
			if time.Since(t) > 7*24*time.Hour {
				delete(st.Protected, k)
			}
		}
		st.Protected[key] = time.Now()
	})
	return nil
}

// ---- conflicts ---------------------------------------------------------------

// conflictCounts caches per-folder conflict counts; walking every save folder
// on each UI refresh would be wasteful.
type conflictCache struct {
	at     time.Time
	counts map[string]int // folder id -> conflict copies
}

func (a *App) conflictCounts(fs []backup.Folder) map[string]int {
	a.mu.Lock()
	cc := a.conflicts
	a.mu.Unlock()
	if cc != nil && time.Since(cc.at) < 30*time.Second {
		return cc.counts
	}
	m := map[string]int{}
	for _, f := range fs {
		n := conflict.Count(f.Path)
		if n > 0 && conflict.DropIdentical(f.Path, fsx.SameContent) > 0 {
			n = conflict.Count(f.Path) // nothing to choose between identical copies
		}
		if n > 0 {
			m[f.ID] = n
		}
	}
	a.mu.Lock()
	a.conflicts = &conflictCache{at: time.Now(), counts: m}
	a.mu.Unlock()
	return m
}

func (a *App) forgetConflicts() {
	a.mu.Lock()
	a.conflicts = nil
	a.mu.Unlock()
}

func folderByID(id string) (backup.Folder, error) {
	fs, err := backupFolders()
	for _, f := range fs {
		if f.ID == id {
			return f, nil
		}
	}
	if err != nil {
		return backup.Folder{}, err
	}
	return backup.Folder{}, errors.New("unknown folder")
}

// Conflicts lists a folder's conflict copies (two versions of the same save).
func (a *App) Conflicts(id string) ([]conflict.Conflict, error) {
	f, err := folderByID(id)
	if err != nil {
		return nil, err
	}
	cs := conflict.Find(f.Path)
	names := a.deviceNames()
	c, err := a.client()
	if err != nil {
		for i := range cs {
			cs[i].DeviceName = names[cs[i].Device]
		}
		return cs, nil
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	// Say which version is this PC's: "keep mine" is what people look for.
	if st, err := c.Status(ctx); err == nil && len(st.MyID) >= 7 {
		me := st.MyID[:7]
		names[me] = cmp.Or(names[me], "this PC")
		if names[me] != "this PC" {
			names[me] += " (this PC)"
		}
	}
	for i := range cs {
		cs[i].DeviceName = names[cs[i].Device]
		if i < 50 { // one Syncthing call each
			if fi, err := c.DBFile(ctx, id, filepath.ToSlash(cs[i].Rel)); err == nil && fi.ModifiedBy != "" {
				cs[i].CurrentName = names[fi.ModifiedBy]
			}
		}
	}
	return cs, nil
}

// deviceNames maps short (7 character) device ids to names.
func (a *App) deviceNames() map[string]string {
	m := map[string]string{}
	for id, n := range meta.Peers() {
		if len(id) >= 7 && n != "" {
			m[id[:7]] = n
		}
	}
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		defer cancel()
		if ds, err := c.Devices(ctx); err == nil {
			for _, d := range ds {
				if len(d.DeviceID) >= 7 && d.Name != "" {
					m[d.DeviceID[:7]] = d.Name
				}
			}
		}
	}
	return m
}

// ResolveConflict keeps one version of a save. useCopy picks the conflict
// copy; otherwise the current file stays. The other version is moved into
// history (Google Drive backup, or the folder's .stversions), never deleted.
// The result syncs to the other PCs.
func (a *App) ResolveConflict(id, copyRel string, useCopy bool) error {
	if err := a.resolveConflict(id, copyRel, useCopy); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// ResolveConflicts settles a folder's conflicts at once, for a save made of
// several files: with device "" the current version of every file stays,
// otherwise every version the PC with that short device id made is used
// (files with versions from more than one other PC are left alone). It
// returns how many were settled.
func (a *App) ResolveConflicts(id, device string) (int, error) {
	f, err := folderByID(id)
	if err != nil {
		return 0, err
	}
	cs := conflict.Find(f.Path)
	if device != "" {
		cs = conflict.FromDevice(cs, device)
	}
	n := 0
	for _, c := range cs {
		if err := a.resolveConflict(id, c.Copy, device != ""); err != nil {
			if n > 0 {
				runtime.EventsEmit(a.ctx, "changed")
			}
			return n, fmt.Errorf("%s: %w", c.Rel, err)
		}
		n++
	}
	runtime.EventsEmit(a.ctx, "changed")
	return n, nil
}

// resolveConflict is ResolveConflict without telling the window.
func (a *App) resolveConflict(id, copyRel string, useCopy bool) error {
	f, err := folderByID(id)
	if err != nil {
		return err
	}
	keep, where := keeper(f)
	curName, copyName := a.versionNames(id, copyRel)
	put, err := conflict.Resolve(f.Path, copyRel, useCopy, keep)
	if err != nil {
		return err
	}
	logx.Printf("conflict in %s: kept the %s version of %s, other one moved to %s", f.Label,
		map[bool]string{true: "other", false: "current"}[useCopy], copyRel, where)
	d := store.Decision{Folder: id, Rel: conflictRel(copyRel), Kept: curName, Other: copyName, Put: put}
	if useCopy {
		d.Kept, d.Other = copyName, curName
	}
	decided(d)
	a.forgetConflicts()
	return nil
}

// conflictRel is the save file a conflict copy is of.
func conflictRel(copyRel string) string {
	orig, _, _ := conflict.Parse(filepath.Base(copyRel))
	return filepath.Join(filepath.Dir(filepath.Clean(copyRel)), orig)
}

// versionNames names the PCs the current file and the conflict copy came
// from ("" when unknown).
func (a *App) versionNames(id, copyRel string) (current, copy string) {
	names := a.deviceNames()
	if _, dev, ok := conflict.Parse(filepath.Base(copyRel)); ok {
		copy = names[dev]
	}
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		defer cancel()
		if fi, err := c.DBFile(ctx, id, filepath.ToSlash(conflictRel(copyRel))); err == nil {
			current = names[fi.ModifiedBy]
		}
	}
	return current, copy
}

// keeper puts files of f into history: its backup history, or the folder's
// .stversions without a backup folder (or while a long backup runs).
func keeper(f backup.Folder) (conflict.Preserve, string) {
	local := conflict.StVersionsKeep(f.Path)
	keep, where := local, filepath.Join(f.Path, ".stversions")
	if t, ok := backupTarget(store.LoadSettings()); ok {
		if err := os.MkdirAll(t, 0o755); err == nil {
			keep = func(abs, rel string, move bool) (string, error) {
				if backup.Running() {
					// Don't make the user wait for it (per file, for "all").
					logx.Printf("conflict in %s: backup busy, keeping the other version in .stversions", f.Label)
					return local(abs, rel, move)
				}
				put, err := backup.Keep(t, f.ID, abs, rel, move)
				if errors.Is(err, backup.ErrBusy) {
					// A long backup is running: don't make the user wait.
					logx.Printf("conflict in %s: backup busy, keeping the other version in .stversions", f.Label)
					return local(abs, rel, move)
				}
				return put, err
			}
			where = "backup history"
		}
	}
	return keep, where
}
