package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

// Backups in the backup folder that no game on this PC uses: games removed
// here, or ones another PC backs up to the same Google account. They can be
// added back (optionally restoring their saves) or deleted.

type othersCache struct {
	at   time.Time
	list []backup.Orphan
}

// knownBackups lists the backup ids that belong to a game on this PC.
func knownBackups() (map[string]bool, error) {
	s := store.LoadSettings()
	fs, err := backupFolders()
	if err != nil && !s.SyncDisabled {
		return nil, fmt.Errorf("can't tell which games are synced here: %w", err)
	}
	known := map[string]bool{}
	for _, f := range fs {
		known[f.ID] = true
	}
	for _, lf := range s.BackupOnly {
		known[lf.ID], known[lf.SyncID], known[lf.CopiedFrom] = true, true, true
	}
	return known, nil
}

// OtherBackups lists backups no game here uses. The list walks the backup
// folder, so it's cached for a few minutes unless refresh is set.
func (a *App) OtherBackups(refresh bool) ([]backup.Orphan, error) {
	target, ok := backupTarget(store.LoadSettings())
	if !ok {
		return nil, errors.New("the backup folder isn't reachable")
	}
	a.mu.Lock()
	oc := a.others
	a.mu.Unlock()
	if !refresh && oc != nil && time.Since(oc.at) < 5*time.Minute {
		return oc.list, nil
	}
	known, err := knownBackups()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	list := backup.Orphans(ctx, target, func(id string) bool { return known[id] })
	if ctx.Err() != nil {
		return list, nil // cut short (a slow Drive): shown, but not kept
	}
	a.mu.Lock()
	a.others = &othersCache{at: time.Now(), list: list}
	a.mu.Unlock()
	return list, nil
}

func (a *App) forgetOthers() {
	a.mu.Lock()
	a.others = nil
	a.mu.Unlock()
}

// otherBackup checks that id is a backup no game here uses.
func otherBackup(target, id string) error {
	if !paths.ValidID(id) {
		return errors.New("unknown backup")
	}
	known, err := knownBackups()
	if err != nil {
		return err
	}
	if known[id] {
		return errors.New("that backup belongs to a game in Syncer; use that game instead")
	}
	if !isDir(filepath.Join(target, id)) && !isDir(filepath.Join(target, backup.VersionsDir, id)) {
		return errors.New("that backup is gone")
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// AdoptBackup adds a game from another backup: backed up (not synced) on this
// PC from path, with that backup's history. With restore, the latest backup
// is copied into path first (files already there are kept as a restore
// point). It returns how many files were restored.
func (a *App) AdoptBackup(id, label, path string, restore bool) (int, error) {
	s := store.LoadSettings()
	target, ok := backupTarget(s)
	if !ok {
		return 0, errors.New("the backup folder isn't reachable")
	}
	if err := otherBackup(target, id); err != nil {
		return 0, err
	}
	if label = strings.TrimSpace(label); label == "" {
		label = backup.LabelFor(target, id)
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return 0, errors.New("choose where the saves go")
	}
	path = filepath.Clean(path)
	if err := paths.CheckSyncable(path); err != nil {
		return 0, err
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return 0, err
		}
		created = true
	}
	taken, backupOnly, err := checkNewFolder(path, currentSynced())
	if err == nil && backupOnly != "" {
		err = errors.New("that folder is already backed up")
	}
	if err != nil {
		if created {
			_ = os.Remove(path)
		}
		return 0, err
	}

	// This PC's own old backup is simply taken back. Another PC's may still be
	// in use there: its history is copied, so the two never mix.
	newID, from := id, ""
	if !ownBackupID(id) || taken[id] {
		newID, from = backupOnlyID(label, taken), id
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.BackupOnly[newID] = store.LocalFolder{ID: newID, Label: label, Path: path, CopiedFrom: from}
		delete(s.NoBackup, newID)
	}); err != nil {
		return 0, err
	}
	a.forgetOthers()
	a.forgetDetails()
	defer runtime.EventsEmit(a.ctx, "changed")
	logx.Printf("added %s (%s) from the backup %s", label, path, id)
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	if from != "" {
		if err := backup.CopyHistory(ctx, target, from, newID); err != nil {
			return 0, fmt.Errorf("added %s, but its backup couldn't be copied yet (it's retried with the next backup): %w", label, err)
		}
	}
	if !restore {
		return 0, nil
	}
	n, err := backup.Restore(target, backup.Folder{ID: newID, Label: label, Path: path}, time.Time{})
	if err != nil {
		return n, fmt.Errorf("added %s, but restoring its saves failed: %w", label, err)
	}
	logx.Printf("restored %d files into %s", n, label)
	return n, nil
}

// ownBackupID reports whether id is a backup-only id this PC made (see
// backupOnlyID): "<game>--<this pc>" or "<game>--<this pc>-<n>".
func ownBackupID(id string) bool {
	i := strings.LastIndex(id, "--")
	if i < 0 {
		return false
	}
	suffix, h := id[i+2:], hostSuffix()
	if suffix == h {
		return true
	}
	n, ok := strings.CutPrefix(suffix, h+"-")
	_, err := strconv.Atoi(n)
	return ok && err == nil
}

// DeleteOtherBackup deletes a backup no game here uses, with its history.
// confirm must be its name as listed.
func (a *App) DeleteOtherBackup(id, confirm string) error {
	target, ok := backupTarget(store.LoadSettings())
	if !ok {
		return errors.New("the backup folder isn't reachable")
	}
	if err := otherBackup(target, id); err != nil {
		return err
	}
	label := backup.LabelFor(target, id)
	if !sameName(confirm, label) {
		return errors.New("type the backup's name to confirm")
	}
	unlock, err := backup.Lock()
	if err != nil {
		return errors.New("a backup is running; try again when it has finished")
	}
	defer unlock()
	if err := backup.Forget(target, id, true); err != nil {
		return err
	}
	logx.Printf("deleted the backup of %s (%s)", label, id)
	a.forgetOthers()
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}
