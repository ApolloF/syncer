package main

// A launcher's own data: a launcher such as Seaglass keeps playtime,
// achievements and settings in a folder of its own and asks Syncer (through
// the launcher API) to sync it between PCs and back it up like a game's
// saves. The launcher keeps each account's data apart inside the folder
// (<account id>\<pc>.json, "shared" when no account plays), so the folder is
// never split per account; Accounts lists each account's part.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// apiLauncherData answers launcherData.
//
// The launcher data contract: the launcher keeps one file per PC and
// account in the folder, so no two PCs ever write the same file, and it adds
// up every file it finds there (playtime summed, achievements joined, the
// newest settings win), skipping Syncthing's conflict copies
// (".sync-conflict-"). Syncer relies on it to bring back old data next to
// new without anything counting twice (see launcherrepair.go).
type apiLauncherData struct {
	ID     string `json:"id,omitempty"`
	Label  string `json:"label,omitempty"`
	Sync   bool   `json:"sync"` // synced with the other PCs (false while Error says why not)
	Backup bool   `json:"backup"`
	Added  bool   `json:"added"` // added by this call
	// Dismissed: the user stopped syncing it; Syncer leaves it that way.
	Dismissed bool `json:"dismissed"`
	// Error says what keeps Syncthing from syncing the folder, in plain
	// words.
	Error string `json:"error,omitempty"`
	// Repaired: this call found the folder emptied or replaced outside
	// Syncer, brought back what the backup had and started syncing it again.
	Repaired bool `json:"repaired,omitempty"`
}

// checkLauncherData says why path can't be a launcher's data folder: it must
// be a subfolder of the launcher's own folder in the user's AppData (Roaming
// or Local), %APPDATA%\<launcher>\Data, so a launcher can't have Syncer
// sync another program's data.
func checkLauncherData(launcher, path string) (string, error) {
	if _, ok := meta.KnownLauncher(launcher); !ok {
		return "", errors.New("Syncer doesn't know this launcher")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("the folder must be a full path")
	}
	path = filepath.Clean(path)
	root, rel, ok := paths.Portable(path)
	if !ok || (root != paths.Roaming && root != paths.Local) {
		return "", errors.New("the folder must be in AppData")
	}
	if parts := strings.Split(rel, "/"); len(parts) < 2 || !strings.EqualFold(parts[0], launcher) {
		return "", errors.New("the folder must be a subfolder of the launcher's own folder")
	}
	if err := paths.CheckSyncable(path); err != nil {
		return "", err
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("the folder doesn't exist")
	}
	// No junction or link on the way (Lstat only looks at the last name).
	if real, err := filepath.EvalSymlinks(path); err != nil || !strings.EqualFold(filepath.Clean(real), path) {
		return "", errors.New("the folder must be a real folder, not a link")
	}
	return path, nil
}

var launcherDataMu sync.Mutex

func isLauncherData(s store.Settings, id string) bool {
	_, ok := s.Launchers[id]
	return ok
}

// launcherData has Syncer sync and back up a launcher's data folder, or
// reports how it's doing when it does already. It doesn't add it again
// once the user stopped syncing it.
func (a *App) launcherData(ctx context.Context, launcher, path string) (apiLauncherData, error) {
	path, err := checkLauncherData(launcher, path)
	if err != nil {
		return apiLauncherData{}, err
	}
	launcher, _ = meta.KnownLauncher(launcher)
	// One at a time: two calls at once would both try to add it.
	launcherDataMu.Lock()
	defer launcherDataMu.Unlock()
	label := launcher + " (playtime, achievements, settings)"
	s := store.LoadSettings()
	mark := func(id string) {
		if s.Launchers[id] != launcher {
			_, _ = store.UpdateSettings(func(s *store.Settings) { s.Launchers[id] = launcher })
		}
	}
	for id, lf := range s.BackupOnly {
		if strings.EqualFold(filepath.Clean(lf.Path), path) {
			mark(id)
			// Stopped syncing (backed up only): it stays that way.
			return apiLauncherData{ID: id, Label: lf.Label, Backup: !s.NoBackup[id], Dismissed: true}, nil
		}
	}
	c, err := a.client()
	if err != nil {
		return apiLauncherData{}, errors.New("Syncthing isn't running")
	}
	// A repair that took the folder out of Syncthing adds it back first, so
	// it isn't added again as a new folder below.
	if finishRejoins(ctx, c) {
		a.emitChanged()
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return apiLauncherData{}, errors.New("Syncthing isn't running")
	}
	for _, f := range fs {
		if f.ID != meta.FolderID && strings.EqualFold(filepath.Clean(f.Path), path) {
			mark(f.ID)
			return a.launcherDataState(ctx, c, f, launcher, !s.NoBackup[f.ID]), nil
		}
	}
	for id, lf := range store.LoadState().Rejoin {
		if strings.EqualFold(filepath.Clean(lf.Path), path) {
			// Adding it back failed just now; it keeps its id for the next try.
			return apiLauncherData{ID: id, Label: lf.Label, Backup: !s.NoBackup[id],
				Error: "Syncer couldn't start syncing it again yet. It tries again later."}, nil
		}
	}
	if s.Dismissed[dismissKey(path)] {
		return apiLauncherData{Dismissed: true}, nil
	}
	if s.SyncDisabled {
		return apiLauncherData{}, errors.New("syncing is turned off in Syncer")
	}
	id, err := addFolder(ctx, c, label, path)
	if err != nil {
		return apiLauncherData{}, fmt.Errorf("couldn't sync it: %w", err)
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		s.Launchers[id] = launcher
		delete(s.Ignored, id)
		delete(s.NoBackup, id)
	})
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c) // added mid-pause: it waits too
	meta.RefreshCache(ctx, c)
	logx.Printf("syncing %s's data (%s)", launcher, path)
	a.emitChanged()
	a.refreshTray()
	return apiLauncherData{ID: id, Label: label, Sync: true, Backup: true, Added: true}, nil
}

// launcherDataState reports how a synced launcher data folder is doing,
// repairing it first when it was emptied or replaced outside Syncer (see
// launcherrepair.go).
func (a *App) launcherDataState(ctx context.Context, c *syncthing.Client, f syncthing.Folder, launcher string, backupOn bool) apiLauncherData {
	d := apiLauncherData{ID: f.ID, Label: cmpOr(f.Label, f.ID), Sync: true, Backup: backupOn}
	st, err := c.FolderStatus(ctx, f.ID)
	if err != nil {
		return d
	}
	if needsRepair(f, st) {
		r, err := repairLauncherData(ctx, c, f, launcher)
		if err != nil {
			logx.Printf("couldn't repair %s's data folder: %v", launcher, err)
		}
		if r.Done {
			d.Repaired = true
			a.emitChanged()
		}
		if st, err = c.FolderStatus(ctx, f.ID); err != nil {
			st = syncthing.FolderStatus{} // just added back: nothing known yet
		}
	}
	if p := folderIssue(ctx, c, f.ID, st); p != "" {
		d.Sync, d.Error = false, p
	}
	return d
}
