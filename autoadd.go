package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

var autoAddMu sync.Mutex

// autoAdd starts syncing every newly detected game: recognised by the game
// database, not already synced, not one the user stopped syncing, installed
// here when "only installed games" is on, and not already in Steam Cloud
// (unless IncludeSteamCloud is on). Games that support Steam Cloud but can't
// be confirmed to use it here are added too, while a Steam emulator's copy of
// saves the game keeps in its own folder is not. Games whose saves are in OneDrive
// are backed up only: OneDrive already syncs them, and two sync tools on the
// same files make conflicting copies. It returns the games synced and the
// games backed up only.
func autoAdd(ctx context.Context, c *syncthing.Client) (added, backedUp []string, err error) {
	autoAddMu.Lock()
	defer autoAddMu.Unlock()
	s := store.LoadSettings()
	if !s.AutoAdd || s.SyncDisabled || s.Paused() {
		return nil, nil, nil
	}
	es, err := discover.Manifest(false)
	if err != nil {
		return nil, nil, err
	}
	installed := func(string) bool { return true }
	if s.InstalledOnly {
		installed = cachedInstalled().Has
	}
	for _, g := range discover.Scan(es) {
		if ctx.Err() != nil {
			break
		}
		if !wantAuto(g, s, installed) {
			continue
		}
		if g.OneDrive {
			id, err := addBackupOnly(g.Name, g.Path, currentSynced())
			if err != nil {
				var ce coveredError
				if !errors.As(err, &ce) {
					logx.Printf("auto-add %s: %v", g.Name, err)
				}
				continue
			}
			backedUp = append(backedUp, g.Name)
			logx.Printf("auto-added %s (%s) as %s, backed up only: it's in OneDrive", g.Name, g.Path, id)
			continue
		}
		id, err := addFolder(ctx, c, g.Name, g.Path)
		if err != nil {
			var ce coveredError
			if !errors.As(err, &ce) {
				logx.Printf("auto-add %s: %v", g.Name, err)
			}
			continue
		}
		added = append(added, g.Name)
		logx.Printf("auto-added %s (%s) as %s", g.Name, g.Path, id)
	}
	if len(added) > 0 {
		_, _ = meta.Reconcile(ctx, c)
	}
	return added, backedUp, nil
}

// runAutoAdd runs autoAdd for the open window and tells the UI what changed.
func (a *App) runAutoAdd() {
	c, err := a.client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	added, backedUp, err := autoAdd(ctx, c)
	if err != nil {
		if !errors.Is(err, syncthing.ErrNotRunning) {
			logx.Printf("auto-add: %v", err)
		}
		return
	}
	if len(added) > 0 {
		runtime.EventsEmit(a.ctx, "toast", "Now syncing new games: "+strings.Join(added, ", "))
	}
	if len(backedUp) > 0 {
		runtime.EventsEmit(a.ctx, "toast", "Backing up new games OneDrive already syncs: "+strings.Join(backedUp, ", "))
	}
	if len(added)+len(backedUp) > 0 {
		runtime.EventsEmit(a.ctx, "changed")
		runtime.EventsEmit(a.ctx, "games:added")
	}
}

func wantAuto(g discover.Found, s store.Settings, installed func(string) bool) bool {
	// Folders over the size limit are left for the user to add by hand: they
	// are more often a misdetection (mods, caches, a whole install).
	if !g.Known || g.Files == 0 || (s.AutoAddMaxGB > 0 && g.Size > int64(s.AutoAddMaxGB)<<30) {
		return false
	}
	if g.SteamCloud && !s.IncludeSteamCloud {
		return false
	}
	// The game's own save folder already holds these saves.
	if g.CopyOf != "" {
		return false
	}
	if s.Dismissed[dismissKey(g.Path)] {
		return false
	}
	// Saves left behind by a game that isn't installed (or that a stopped
	// sync left here) wait until it is: "Stop syncing them" relies on this.
	if s.InstalledOnly && !installed(g.Name) {
		return false
	}
	// Stopped syncing before "dismissed" existed: the folder id is ignored.
	return !s.Ignored[meta.NewID(g.Name, nil)]
}

// dismissKey identifies a folder the same way on every PC.
func dismissKey(p string) string {
	if root, rel, ok := paths.Portable(p); ok {
		return strings.ToLower(root + ":" + rel)
	}
	return strings.ToLower(filepath.Clean(p))
}

// coveredError: the folder is already synced (itself, as part of a parent, or
// it holds a synced folder).
type coveredError string

func (e coveredError) Error() string { return string(e) }

// addFolder creates a synced folder for path, shared with every paired PC,
// and returns its id. A path that is backed up only counts as covered: turning
// its sync on is the caller's call (see AddFolder).
func addFolder(ctx context.Context, c *syncthing.Client, label, path string) (string, error) {
	path = filepath.Clean(path)
	st, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return "", err
	}
	var synced []backup.Folder
	for _, f := range fs {
		if f.ID != meta.FolderID {
			synced = append(synced, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
		}
	}
	taken, backupOnly, err := checkNewFolder(path, synced)
	if err != nil {
		return "", err
	}
	if backupOnly != "" {
		return "", coveredError("already backed up only")
	}
	taken[meta.FolderID] = true
	ds, _ := c.Devices(ctx)
	var others []string
	for _, d := range ds {
		if d.DeviceID != st.MyID {
			others = append(others, d.DeviceID)
		}
	}
	if label == "" {
		label = filepath.Base(path)
	}
	id := syncID(st.MyID, label, path, taken)
	if err := meta.AddFolder(ctx, c, id, label, path, st.MyID, others); err != nil {
		return "", err
	}
	return id, nil
}

// syncID picks the id a folder syncs under: the one another PC already uses
// for the same saves, so both PCs share one folder (a new id there would make
// each PC skip the other's copy as overlapping), else a new one from label.
func syncID(me, label, path string, taken map[string]bool) string {
	if sf, ok := meta.PeerFolderAt(me, path); ok && !taken[sf.ID] {
		return sf.ID
	}
	return meta.NewID(label, taken)
}
