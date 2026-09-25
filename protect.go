package main

import (
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
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

func init() { meta.BeforeJoin = protectExisting }

// snapshotDir holds pre-sync snapshots when no backup folder is available.
func snapshotDir() string { return filepath.Join(paths.Root(paths.Local), "Syncer", "snapshots") }

// protectExisting saves the files already in path as a restore point before
// this PC starts exchanging them with other PCs. If the other PC's copy wins,
// this PC's saves can still be restored ("As it was before <time>").
func protectExisting(ctx context.Context, id, label, path string) error {
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
		if n := conflict.Count(f.Path); n > 0 {
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
	for i := range cs {
		cs[i].DeviceName = names[cs[i].Device]
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
	f, err := folderByID(id)
	if err != nil {
		return err
	}
	keep := conflict.StVersionsKeep(f.Path)
	where := filepath.Join(f.Path, ".stversions")
	if t, ok := backupTarget(store.LoadSettings()); ok {
		if err := os.MkdirAll(t, 0o755); err == nil {
			keep = func(abs, rel string) error { return backup.Keep(t, f.ID, abs, rel) }
			where = "backup history"
		}
	}
	if err := conflict.Resolve(f.Path, copyRel, useCopy, keep); err != nil {
		return err
	}
	logx.Printf("conflict in %s: kept the %s version of %s, other one moved to %s", f.Label,
		map[bool]string{true: "other", false: "current"}[useCopy], copyRel, where)
	a.forgetConflicts()
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}
