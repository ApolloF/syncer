package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// DeleteSaves moves a game's save folder on this PC to the Recycle Bin. A
// synced game stops syncing here first, so the deletion never reaches your
// other PCs. A backed-up game is backed up one last time first and nothing is
// deleted if that fails. Afterwards the game stays listed as backup-only, so
// its saves can be restored. confirm must be the game's name; noBackupOK must
// be set for a game without a backup. It returns the game's id afterwards.
func (a *App) DeleteSaves(id, confirm string, noBackupOK bool) (string, error) {
	ctx, cancel := joinCtx(a.ctx) // the last backup can take a while
	defer cancel()
	s := store.LoadSettings()
	var c *syncthing.Client
	var label, path string
	if lf, ok := s.BackupOnly[id]; ok {
		label, path = lf.Label, lf.Path
	} else {
		cl, err := a.client()
		if err != nil {
			return id, err
		}
		f, err := findFolder(ctx, cl, id)
		if err != nil {
			return id, err
		}
		c, label, path = cl, cmpOr(f.Label, f.ID), f.Path
	}
	if !sameName(confirm, label) {
		return id, errors.New("type the game's name to confirm")
	}
	target, hasTarget := backupTarget(s)
	backedUp := hasTarget && !s.NoBackup[id]
	if !backedUp && !noBackupOK {
		return id, errors.New("this game isn't backed up; confirm that its saves can only come back from the Recycle Bin")
	}
	if !hasTarget {
		target = ""
	}
	all, err := backupFolders()
	if err != nil && !s.SyncDisabled {
		return id, fmt.Errorf("can't check which folders are synced, so nothing was deleted: %w", err)
	}
	var others []string
	for _, f := range all {
		if f.ID != id {
			others = append(others, f.Path)
		}
	}
	if err := deletableDir(path, others, target); err != nil {
		return id, err
	}

	// The last backup comes first: if it fails, nothing has changed yet.
	if backedUp {
		f := backup.Folder{ID: id, Label: label, Path: path, Exclude: s.Exclude[dismissKey(path)]}
		res, err := backup.Run(ctx, []backup.Folder{f}, backup.Options{Target: target, KeepDays: s.KeepDays})
		if err == nil && len(res.Errors) > 0 {
			err = errors.New(res.Errors[0])
		}
		if err != nil {
			return id, fmt.Errorf("couldn't back %s up first, so nothing was changed: %w", label, err)
		}
		store.UpdateState(func(st *store.State) { recordFolders(st, res) })
	}

	stillThere := "the saves are still there"
	if c != nil {
		bid, err := a.stopSync(ctx, c, id) // carries the backup over to the new id
		if err != nil {
			return id, err
		}
		id = bid
		stillThere += ", but " + label + " no longer syncs on this PC (turn Sync back on to share it again)"
		runtime.EventsEmit(a.ctx, "changed")
		// Deleting a folder Syncthing still has would delete it on every PC.
		fs, err := c.Folders(ctx)
		if err != nil {
			return id, fmt.Errorf("couldn't confirm %s stopped syncing, so nothing was deleted: %w", label, err)
		}
		for _, f := range fs {
			if paths.Within(f.Path, path) || paths.Within(path, f.Path) {
				return id, fmt.Errorf("%s is still synced, so nothing was deleted", label)
			}
		}
	}

	if err := winx.Recycle(path); err != nil {
		if errors.Is(err, winx.ErrCancelled) {
			return id, errors.New("cancelled; " + stillThere)
		}
		return id, fmt.Errorf("couldn't move the saves of %s to the Recycle Bin: %w", label, err)
	}
	if _, err := os.Lstat(path); err == nil {
		return id, fmt.Errorf("some saves of %s couldn't be moved to the Recycle Bin (is the game running?)", label)
	}
	logx.Printf("moved the saves of %s (%s) to the Recycle Bin", label, path)
	a.forgetConflicts()
	a.forgetDetails()
	runtime.EventsEmit(a.ctx, "changed")
	return id, nil
}

// sameName compares a typed confirmation with a game's name.
func sameName(typed, name string) bool {
	typed, name = strings.TrimSpace(typed), strings.TrimSpace(name)
	return name != "" && strings.EqualFold(typed, name)
}

// deletableDir checks that path is a save folder that may be deleted: a real
// directory (not a link) where a save folder may be at all, holding no other
// game's folder and not inside one, and apart from the backup folder.
func deletableDir(path string, others []string, target string) error {
	if !filepath.IsAbs(path) {
		return errors.New("the save folder's location is unknown")
	}
	path = filepath.Clean(path)
	fi, err := os.Lstat(path)
	if err != nil {
		return errors.New("the save folder isn't on this PC")
	}
	if !fi.IsDir() {
		return errors.New("the save folder is a link or a file, so Syncer won't delete it")
	}
	if err := paths.CheckSyncable(path); err != nil {
		return fmt.Errorf("Syncer won't delete %s: %w", path, err)
	}
	for _, o := range others {
		if o != "" && (paths.Within(o, path) || paths.Within(path, o)) {
			return fmt.Errorf("the save folder overlaps another game's folder (%s), so nothing was deleted", o)
		}
	}
	if target != "" && (paths.Within(target, path) || paths.Within(path, target)) {
		return errors.New("the save folder overlaps the backup folder, so nothing was deleted")
	}
	return nil
}
