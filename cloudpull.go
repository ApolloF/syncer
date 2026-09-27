package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Syncthing only syncs while both PCs are on. When the PC that played last
// is off, its backup still holds its saves: Syncer takes them from there
// (like a cloud save), but only when that can't cost anything. The saves
// here must be exactly what this PC last backed up (so nothing here is newer
// or unsaved), no game may be running, that PC's backup must have fully
// arrived through Google Drive, and Syncthing mustn't be bringing the saves
// itself. The saves here are kept as a restore point first. Otherwise the
// game offers "Get it", and says why it didn't happen by itself.

// pullCheck is what decides whether a newer save is taken from the backup.
type pullCheck struct {
	Auto      bool // taking newer saves by itself is on
	Paused    bool // syncing is paused (here or for this game)
	Online    bool // the PC that made the save is connected: Syncthing brings it
	Incoming  bool // Syncthing is fetching changes for this game right now
	Ready     bool // that PC's whole backup has arrived here through Drive
	Playing   bool // a game is running
	Conflicts int  // two versions of a save wait to be settled
	Changed   bool // the saves here changed since this PC's last backup
}

// pullVerdict says whether to take the newer save now (auto), whether the
// user may take it (canGet), and if not by itself, why.
func pullVerdict(c pullCheck) (auto, canGet bool, why string) {
	switch {
	case c.Online || c.Incoming:
		return false, false, "it's arriving through Syncthing"
	case !c.Ready:
		return false, false, "its backup hasn't fully reached this PC through Google Drive yet"
	case c.Playing:
		return false, false, "a game is running"
	case c.Conflicts > 0:
		return false, false, "settle the two versions of this game's saves first"
	case c.Changed:
		return false, true, "the saves here changed since this PC's last backup"
	case c.Paused:
		return false, true, "syncing is paused"
	case !c.Auto:
		return false, true, "taking newer saves by itself is off"
	}
	return true, true, ""
}

// pullNewer looks for newer saves of every synced game in other PCs'
// backups, takes the ones it safely can, and returns the rest (and the games
// taken). playing reports whether a game is running.
func pullNewer(ctx context.Context, playing func() bool) (left map[string]newerSave, took []string) {
	left = map[string]newerSave{}
	s := store.LoadSettings()
	target, ok := backupTarget(s)
	if !ok || s.SyncDisabled {
		return left, nil
	}
	fs, err := syncedFolders()
	if err != nil {
		return left, nil
	}
	c, _ := syncthing.New()
	for _, f := range fs {
		if ctx.Err() != nil {
			return left, took
		}
		f.Exclude = s.Exclude[dismissKey(f.Path)]
		n, in, ok := newerElsewhere(target, f, f.Exclude, time.Now())
		if !ok {
			continue
		}
		chk, files := gatherPull(ctx, c, target, f, in, s, playing)
		auto, canGet, why := pullVerdict(chk)
		if auto {
			k, err := takeNewer(ctx, c, target, f, in, files)
			if err == nil {
				logx.Printf("took the newer save of %s (%d file(s)) from %s's backup", f.Label, k, n.Host)
				took = append(took, f.Label)
				continue
			}
			logx.Printf("take the newer save of %s from %s's backup: %v", f.Label, n.Host, err)
			why = err.Error()
		}
		n.CanGet, n.Why = canGet, why
		left[f.ID] = n
	}
	return left, took
}

// gatherPull collects what pullVerdict needs about f and the newer save in.
func gatherPull(ctx context.Context, c *syncthing.Client, target string, f backup.Folder, in backup.Info, s store.Settings, playing func() bool) (pullCheck, []backup.FileEntry) {
	chk := pullCheck{Auto: !s.NoCloudPull, Paused: s.Paused(), Conflicts: conflict.Count(f.Path)}
	files, err := backup.ReadFiles(target, in)
	chk.Ready = err == nil && backup.MirrorMatches(target, f.ID, files)
	if c != nil {
		if conns, err := c.Connections(ctx); err == nil && in.Device != "" {
			chk.Online = conns.Connections[in.Device].Connected
		}
		if st, err := c.FolderStatus(ctx, f.ID); err == nil {
			chk.Incoming = st.NeedBytes > 0 || st.NeedFiles > 0
			chk.Paused = chk.Paused || st.State == "paused"
		}
	}
	if chk.Ready && !chk.Online && !chk.Incoming {
		// Only worth the disk reads when nothing else already says no.
		chk.Playing = playing()
		chk.Changed = !backup.LocalMatchesIndex(f)
	}
	return chk, files
}

// takeNewer keeps the saves here as a restore point, then takes the files of
// another PC's backup. Syncthing passes them on to the other PCs.
func takeNewer(ctx context.Context, c *syncthing.Client, target string, f backup.Folder, in backup.Info, files []backup.FileEntry) (int, error) {
	if _, err := backup.Snapshot(ctx, target, f); err != nil {
		return 0, fmt.Errorf("could not keep the saves here first: %w", err)
	}
	n, err := backup.TakeFiles(ctx, target, f, files)
	if err != nil {
		return 0, err
	}
	if c != nil {
		_ = c.Rescan(ctx, f.ID)
	}
	return n, nil
}

// GetNewer takes the newer save another PC backed up for a game, when it
// wasn't taken by itself. The saves here are kept as a restore point first.
func (a *App) GetNewer(id string) (int, error) {
	n, err := a.getNewer(a.ctx, id)
	if err == nil {
		runtime.EventsEmit(a.ctx, "changed")
	}
	return n, err
}

func (a *App) getNewer(ctx context.Context, id string) (int, error) {
	s := store.LoadSettings()
	target, ok := backupTarget(s)
	if !ok {
		return 0, errors.New("no backup folder")
	}
	f, err := folderByID(id)
	if err != nil {
		return 0, err
	}
	f.Exclude = s.Exclude[dismissKey(f.Path)]
	n, in, ok := newerElsewhere(target, f, f.Exclude, time.Now())
	if !ok {
		return 0, errors.New("no newer save on another PC")
	}
	c, _ := syncthing.New()
	chk, files := gatherPull(ctx, c, target, f, in, s, a.gameRunning)
	if _, canGet, why := pullVerdict(chk); !canGet {
		return 0, errors.New("can't take it now: " + why)
	}
	k, err := takeNewer(ctx, c, target, f, in, files)
	if err != nil {
		return 0, err
	}
	logx.Printf("took the newer save of %s (%d file(s)) from %s's backup, on request", f.Label, k, n.Host)
	a.mu.Lock()
	if _, ok := a.newer[id]; ok {
		m := make(map[string]newerSave, len(a.newer))
		for k, v := range a.newer {
			if k != id {
				m[k] = v
			}
		}
		a.newer = m
	}
	a.mu.Unlock()
	return k, nil
}
