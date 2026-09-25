package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Folders bigger than this are left for the user to add by hand: a save folder
// that large is more likely a misdetection (mods, caches, a whole install).
const autoAddMaxBytes = 1 << 30

var autoAddMu sync.Mutex

// autoAdd starts syncing every newly detected game: recognised by the game
// database, not already synced, not one the user stopped syncing, and not
// already in Steam Cloud (unless IncludeSteamCloud is on). Games that support
// Steam Cloud but can't be confirmed to use it here are added too.
func autoAdd(ctx context.Context, c *syncthing.Client) ([]string, error) {
	autoAddMu.Lock()
	defer autoAddMu.Unlock()
	s := store.LoadSettings()
	if !s.AutoAdd {
		return nil, nil
	}
	es, err := discover.Manifest(false)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, g := range discover.Scan(es) {
		if ctx.Err() != nil {
			break
		}
		if !wantAuto(g, s) {
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
	return added, nil
}

// runAutoAdd runs autoAdd for the open window and tells the UI what changed.
func (a *App) runAutoAdd() {
	c, err := a.client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	added, err := autoAdd(ctx, c)
	if err != nil {
		if !errors.Is(err, syncthing.ErrNotRunning) {
			logx.Printf("auto-add: %v", err)
		}
		return
	}
	if len(added) > 0 {
		runtime.EventsEmit(a.ctx, "toast", "Now syncing new games: "+strings.Join(added, ", "))
		runtime.EventsEmit(a.ctx, "changed")
		runtime.EventsEmit(a.ctx, "games:added")
	}
}

func wantAuto(g discover.Found, s store.Settings) bool {
	if !g.Known || g.Size > autoAddMaxBytes || g.Files == 0 {
		return false
	}
	if g.SteamCloud && !s.IncludeSteamCloud {
		return false
	}
	if s.Dismissed[dismissKey(g.Path)] {
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
// and returns its id.
func addFolder(ctx context.Context, c *syncthing.Client, label, path string) (string, error) {
	path = filepath.Clean(path)
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return "", errors.New("folder not found: " + path)
	}
	if _, _, ok := paths.Portable(path); !ok {
		return "", errors.New("only folders inside your user profile, Documents, AppData or Saved Games can be synced between PCs")
	}
	st, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, f := range fs {
		taken[f.ID] = true
		if paths.Within(f.Path, path) {
			return "", coveredError("already synced as part of \"" + f.Label + "\"")
		}
		if paths.Within(path, f.Path) && f.ID != meta.FolderID {
			return "", coveredError("this folder contains \"" + f.Label + "\", which is already synced — remove that first")
		}
	}
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
	id := meta.NewID(label, taken)
	if err := meta.AddFolder(ctx, c, id, label, path, st.MyID, others); err != nil {
		return "", err
	}
	return id, nil
}
