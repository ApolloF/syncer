package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/tasks"
)

// UndoOptions choose how far "Undo everything" goes.
type UndoOptions struct {
	Unpair             bool `json:"unpair"`
	StopBackups        bool `json:"stopBackups"`
	DeleteBackups      bool `json:"deleteBackups"` // requires StopBackups
	StopSyncthing      bool `json:"stopSyncthing"`
	UninstallSyncthing bool `json:"uninstallSyncthing"` // requires StopSyncthing
}

// UndoReport summarises what was undone.
type UndoReport struct {
	Folders int      `json:"folders"`
	Devices int      `json:"devices"`
	Notes   []string `json:"notes"`
}

// UndoAll stops all syncing on this PC and removes what Syncer set up. Save
// files are never deleted; backups only when explicitly asked.
func (a *App) UndoAll(o UndoOptions) (UndoReport, error) {
	a.stopBackup()
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
	defer cancel()
	rep, err := undoAll(ctx, o)
	runtime.EventsEmit(a.ctx, "changed")
	return rep, err
}

// stopBackup cancels a backup started from the window and waits for it.
func (a *App) stopBackup() {
	a.CancelBackup()
	for i := 0; i < 100; i++ {
		a.mu.Lock()
		busy := a.backingUp
		a.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func undoAll(ctx context.Context, o UndoOptions) (UndoReport, error) {
	var rep UndoReport
	note := func(format string, args ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, args...)) }
	o.DeleteBackups = o.DeleteBackups && o.StopBackups
	o.UninstallSyncthing = o.UninstallSyncthing && o.StopSyncthing
	before := store.LoadSettings()
	if _, ok := backupTarget(before); o.DeleteBackups && !ok {
		return rep, errors.New("the backup folder isn't reachable; start Google Drive or untick \"Delete backups\"")
	}
	// Hold the backup lock throughout, so no backup recreates what's deleted.
	var locked bool
	if o.StopBackups {
		unlock, err := backup.Lock()
		if err != nil && o.DeleteBackups {
			return rep, errors.New("a backup is running; try again when it has finished")
		}
		if locked = err == nil; locked {
			defer unlock()
		}
	}

	var c *syncthing.Client
	var me string
	var live []syncthing.Folder
	if cl, err := syncthing.New(); err == nil {
		if st, err := cl.Status(ctx); err == nil {
			if live, err = cl.Folders(ctx); err != nil {
				return rep, err
			}
			c, me = cl, st.MyID
		}
	}
	synced, _ := syncedFolders()

	// Settings first: this stops the window and the background task from
	// re-adding anything while the rest is undone.
	var converted [][2]string // synced id -> backup-only id, to carry history over
	s, err := store.UpdateSettings(func(s *store.Settings) {
		s.SyncDisabled = true
		s.AutoAdd = false // otherwise re-enabling sync would re-add every game at once
		s.Ignored = map[string]bool{}
		if o.StopBackups {
			s.BackupEnabled = false
			s.BackupOnly = map[string]store.LocalFolder{}
			return
		}
		// Keep backing every game up, as backup-only folders.
	next:
		for _, f := range synced {
			if !paths.ValidID(f.ID) || s.NoBackup[f.ID] {
				continue
			}
			taken := map[string]bool{}
			for k, lf := range s.BackupOnly {
				if paths.Within(lf.Path, f.Path) && paths.Within(f.Path, lf.Path) {
					continue next // already backup-only (Undo run twice)
				}
				taken[k] = true
			}
			id := backupOnlyID(cmpOr(f.Label, f.ID), taken)
			s.BackupOnly[id] = store.LocalFolder{ID: id, Label: cmpOr(f.Label, f.ID), Path: f.Path, SyncID: f.ID}
			converted = append(converted, [2]string{f.ID, id})
		}
	})
	if err != nil {
		return rep, err
	}

	if c != nil {
		for _, f := range live {
			if err := c.RemoveFolder(ctx, f.ID); err != nil {
				note("Couldn't stop syncing %s: %v", cmpOr(f.Label, f.ID), err)
				continue
			}
			if f.ID == meta.FolderID {
				continue
			}
			rep.Folders++
			if v := cleanMarkers(f.Path); v != "" {
				note("Kept older synced versions of %s in %s.", cmpOr(f.Label, f.ID), v)
			}
		}
		if err := os.RemoveAll(meta.Dir()); err != nil {
			note("Couldn't remove %s: %v", meta.Dir(), err)
		}
		if o.Unpair {
			rep.Devices = unpairAll(ctx, c, me, note)
		}
	} else {
		note("Syncthing isn't running, so its folders and linked PCs were left as they are. Start it and run Undo again to finish.")
	}

	if o.StopSyncthing {
		if err := tasks.Delete(tasks.SyncthingTask); err != nil {
			note("Couldn't remove Syncthing's autostart: %v", err)
		}
		if c != nil {
			if err := c.Shutdown(ctx); err != nil {
				note("Couldn't stop Syncthing: %v", err)
			}
		}
		if o.UninstallSyncthing {
			if err := syncthing.Uninstall(ctx); err != nil {
				note("Couldn't uninstall Syncthing: %v", err)
			} else {
				note("Syncthing was uninstalled. Its own settings in %s were kept.", filepath.Dir(syncthing.ConfigPath()))
			}
		}
	}

	if o.StopBackups {
		if err := tasks.Delete(tasks.BackupTask); err != nil {
			note("Couldn't remove the background task: %v", err)
		}
		if locked {
			forgetBackups(before, synced, o.DeleteBackups, note)
		}
	} else {
		ensureBackgroundTask()
	}
	// Last, since it can be slow on a cloud drive: everything above must not
	// wait on it.
	for _, cv := range converted {
		keepHistory(ctx, s, cv[0], cv[1])
	}
	if err := meta.ForgetCache(); err != nil {
		note("Couldn't remove the folder cache: %v", err)
	}
	logx.Printf("undo: %d folders, %d devices, stopBackups=%v deleteBackups=%v stopSyncthing=%v uninstall=%v",
		rep.Folders, rep.Devices, o.StopBackups, o.DeleteBackups, o.StopSyncthing, o.UninstallSyncthing)
	return rep, nil
}

// unpairAll removes every linked PC and pending request; returns how many PCs.
func unpairAll(ctx context.Context, c *syncthing.Client, me string, note func(string, ...any)) int {
	n := 0
	ds, err := c.Devices(ctx)
	if err != nil {
		note("Couldn't list linked PCs: %v", err)
		return 0
	}
	for _, d := range ds {
		if d.DeviceID == me {
			continue
		}
		if err := c.RemoveDevice(ctx, d.DeviceID); err != nil {
			note("Couldn't unlink %s: %v", cmpOr(d.Name, d.DeviceID[:7]), err)
			continue
		}
		n++
	}
	if p, err := c.PendingDevices(ctx); err == nil {
		for id := range p {
			_ = c.DismissPendingDevice(ctx, id)
		}
	}
	if n > 0 {
		note("Your other PCs still list this one; remove it there under Devices.")
	}
	return n
}

// forgetBackups drops backup bookkeeping for every folder Syncer knew and,
// with del, deletes their backups (the backup root goes only if left empty).
func forgetBackups(s store.Settings, synced []backup.Folder, del bool, note func(string, ...any)) {
	target, ok := backupTarget(s)
	if del && !ok {
		note("The backup folder isn't reachable, so backups weren't deleted.")
		del = false
	}
	for _, f := range mergeFolders(synced, s.BackupOnly) {
		if !paths.ValidID(f.ID) {
			continue
		}
		if err := backup.Forget(target, f.ID, del); err != nil {
			note("Couldn't delete the backup of %s: %v", f.Label, err)
		}
	}
	if del {
		_ = os.Remove(filepath.Join(target, backup.VersionsDir))
		_ = os.Remove(target)
	}
}
