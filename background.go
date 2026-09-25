package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/tasks"
	"github.com/ApolloF/syncer/internal/winx"
)

// runBackground is what the scheduled task runs: keep Syncthing alive, apply
// shared metadata, then back up to Google Drive. No window is shown. The whole
// process runs at background priority so it never competes with a game.
func runBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	if err := winx.BackgroundProcess(); err != nil {
		logx.Printf("background priority: %v", err)
	}
	logx.Printf("background run started")
	s := store.LoadSettings()

	if !s.SyncDisabled {
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
	}
	if !s.BackupEnabled {
		logx.Printf("backup disabled, done")
		return
	}
	var pause func(context.Context)
	if s.PauseWhileGaming {
		var stop context.CancelCauseFunc
		ctx, stop = context.WithCancelCause(ctx)
		defer stop(nil)
		pause = gamingPause(stop, discover.LoadInstalled())
		pause(ctx)
		if ctx.Err() != nil {
			logx.Printf("backup postponed: a game is still running")
			return
		}
	}
	if _, err := runBackup(ctx, nil, pause); err != nil {
		logx.Printf("backup: %v", err)
	}
}

var errGaming = errors.New("stopped while a game was running; it continues next run")

// gamingPause returns a backup pause hook that holds the backup while a game
// is running and gives up (cancelling with errGaming) shortly before the
// task's time limit, so a long session just moves the backup to the next run.
func gamingPause(stop context.CancelCauseFunc, inst *discover.Installed) func(context.Context) {
	return func(ctx context.Context) {
		waited := false
		for playing(inst) && ctx.Err() == nil {
			if !waited {
				logx.Printf("backup paused: a game is running")
				waited = true
			}
			if d, ok := ctx.Deadline(); ok && time.Until(d) < 3*time.Minute {
				stop(errGaming)
				return
			}
			sleep(ctx, time.Minute)
		}
		if waited && ctx.Err() == nil {
			logx.Printf("backup resumed")
		}
	}
}

// playing reports whether the user is in a game: a full-screen app, or the
// foreground window belongs to a store-installed game. Only the foreground
// counts, since tools like Wallpaper Engine are "games" that run all day.
func playing(inst *discover.Installed) bool {
	return winx.FullScreen() || inst.Running([]string{winx.ForegroundPath()})
}

var instCache struct {
	sync.Mutex
	inst *discover.Installed
	at   time.Time
}

// cachedInstalled avoids rescanning stores and the registry on every UI poll.
func cachedInstalled() *discover.Installed {
	instCache.Lock()
	defer instCache.Unlock()
	if instCache.inst == nil || time.Since(instCache.at) > time.Minute {
		instCache.inst, instCache.at = discover.LoadInstalled(), time.Now()
	}
	return instCache.inst
}

// runBackup backs up every enabled folder and records the result.
func runBackup(ctx context.Context, onProg func(backup.Progress), pause func(context.Context)) (*store.BackupRun, error) {
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
	res, err := backup.Run(ctx, fs, backup.Options{Target: target, KeepDays: s.KeepDays, OnProg: onProg, Pause: pause})
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

// backupFolders lists what to back up: Syncthing's folders (or the cached list
// if it's down) plus this PC's backup-only folders. After "Undo everything"
// Syncthing's list is ignored; its folders were converted to backup-only.
func backupFolders() ([]backup.Folder, error) {
	s := store.LoadSettings()
	var synced []backup.Folder
	var err error
	if !s.SyncDisabled {
		synced, err = syncedFolders()
	}
	if err != nil && len(s.BackupOnly) == 0 {
		return nil, err
	}
	return mergeFolders(synced, s.BackupOnly), nil
}

func syncedFolders() ([]backup.Folder, error) {
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

// mergeFolders appends backup-only folders to the synced ones, skipping any
// whose id or path a synced folder already covers.
func mergeFolders(synced []backup.Folder, backupOnly map[string]store.LocalFolder) []backup.Folder {
	out := append([]backup.Folder(nil), synced...)
	var extra []backup.Folder
next:
	for _, lf := range backupOnly {
		for _, f := range synced {
			if f.ID == lf.ID || strings.EqualFold(filepath.Clean(f.Path), filepath.Clean(lf.Path)) {
				continue next
			}
		}
		extra = append(extra, backup.Folder{ID: lf.ID, Label: lf.Label, Path: lf.Path})
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].ID < extra[j].ID })
	return append(out, extra...)
}

// ensureBackgroundTask (re)registers the scheduled task so it always points at
// the current exe and interval, and removes it once it has nothing left to do.
func ensureBackgroundTask() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if strings.HasSuffix(strings.ToLower(exe), "-dev.exe") {
		return // `wails dev` binary: never point the real task at it
	}
	s := store.LoadSettings()
	if s.SyncDisabled && !s.BackupEnabled {
		if err := tasks.Delete(tasks.BackupTask); err != nil {
			logx.Printf("remove background task: %v", err)
		}
		return
	}
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
