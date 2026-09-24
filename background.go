package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/tasks"
)

// runBackground is what the scheduled task runs: keep Syncthing alive, apply
// shared metadata, then back up to Google Drive. No window is shown.
func runBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	logx.Printf("background run started")

	if err := syncthing.WaitReady(ctx, 3*time.Second); err != nil && syncthing.FindExe() != "" {
		logx.Printf("syncthing not running, starting it")
		if err := syncthing.Start(ctx); err != nil {
			logx.Printf("start syncthing: %v", err)
		}
	}
	if c, err := syncthing.New(); err == nil {
		if rep, err := meta.Reconcile(ctx, c); err != nil {
			logx.Printf("reconcile: %v", err)
		} else if len(rep.Added) > 0 {
			logx.Printf("reconcile: added %v", rep.Added)
		}
	}
	if !store.LoadSettings().BackupEnabled {
		logx.Printf("backup disabled, done")
		return
	}
	if _, err := runBackup(ctx, nil); err != nil {
		logx.Printf("backup: %v", err)
	}
}

// runBackup backs up every enabled folder and records the result.
func runBackup(ctx context.Context, onProg func(backup.Progress)) (*store.BackupRun, error) {
	s := store.LoadSettings()
	target, ok := backup.Target(s.BackupRoot)
	if !ok {
		err := errors.New("Google Drive for desktop not found — install it and sign in, or choose a backup folder")
		record(&store.BackupRun{Started: time.Now(), Finished: time.Now(), Errors: []string{err.Error()}})
		return nil, err
	}
	all, err := backupFolders()
	if err != nil {
		return nil, err
	}
	var fs []backup.Folder
	for _, f := range all {
		if !s.NoBackup[f.ID] {
			fs = append(fs, f)
		}
	}
	res, err := backup.Run(ctx, fs, backup.Options{Target: target, KeepDays: s.KeepDays, OnProg: onProg})
	if err != nil {
		if !errors.Is(err, backup.ErrBusy) {
			record(&store.BackupRun{Started: time.Now(), Finished: time.Now(), Target: target, Errors: []string{err.Error()}})
		}
		return nil, err
	}
	record(res)
	logx.Printf("backup finished: %d copied, %d versioned, %d errors → %s", res.Copied, res.Versions, len(res.Errors), target)
	for i, e := range res.Errors {
		if i == 10 {
			logx.Printf("  … %d more", len(res.Errors)-10)
			break
		}
		logx.Printf("  %s", e)
	}
	return res, nil
}

func record(r *store.BackupRun) {
	st := store.LoadState()
	st.LastBackup = r
	_ = store.SaveState(st)
}

// backupFolders lists folders from Syncthing, or the cached list if it's down.
func backupFolders() ([]backup.Folder, error) {
	var out []backup.Folder
	if c, err := syncthing.New(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if fs, err := c.Folders(ctx); err == nil {
			for _, f := range fs {
				if f.ID != meta.FolderID {
					out = append(out, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
				}
			}
			return out, nil
		}
	}
	cf, err := meta.CachedFolders()
	if err != nil {
		return nil, err
	}
	for _, f := range cf {
		out = append(out, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
	}
	return out, nil
}

// ensureBackgroundTask (re)registers the scheduled task so it always points at
// the current exe and interval.
func ensureBackgroundTask() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	s := store.LoadSettings()
	err = tasks.Register(tasks.Spec{
		Name:        tasks.BackupTask,
		Description: "Syncer: keeps Syncthing running, applies settings from your other PCs and backs up game saves to Google Drive.",
		Exe:         exe,
		Args:        "--background",
		AtLogon:     true,
		LogonDelay:  3 * time.Minute,
		RepeatEvery: time.Duration(s.IntervalHours) * time.Hour,
		TimeLimit:   time.Hour,
	})
	if err != nil {
		logx.Printf("register background task: %v", err)
	}
}

func ensureSyncthingTask() error {
	if tasks.Exists(tasks.SyncthingTask) {
		return nil
	}
	exe := syncthing.FindExe()
	if exe == "" {
		return errors.New("syncthing.exe not found")
	}
	return tasks.Register(tasks.Spec{
		Name:        tasks.SyncthingTask,
		Description: "Starts Syncthing (game save sync) at logon.",
		Exe:         exe,
		Args:        "--no-browser --no-restart",
		AtLogon:     true,
	})
}

// migrateLegacy replaces the original script-based setup once: the old backup
// task and its scripts (one of which held the Syncthing API key in plain text).
func migrateLegacy() {
	if store.LoadSettings().Migrated {
		return
	}
	if err := tasks.Delete(tasks.LegacyBackupTask); err != nil {
		logx.Printf("migrate: %v", err)
		return
	}
	dir := filepath.Join(paths.Root(paths.Local), "Syncthing")
	for _, n := range []string{"setup_folders.ps1", "gdrive_backup.ps1", "rclone_auth.log", "rclone_auth.err"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			if err := os.Remove(p); err == nil {
				logx.Printf("migrate: removed legacy %s", n)
			}
		}
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) { s.Migrated = true })
	logx.Printf("migrate: legacy setup replaced")
}
