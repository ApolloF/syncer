package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/tasks"
)

// A pause stops syncing (every Syncthing folder is paused) and automatic
// backups on this PC for a while. It always ends on its own: a one-off
// scheduled task runs "Syncer.exe --resume" at the end, and the window and
// the background task resume too as soon as they notice the time has passed.

const maxPause = 7 * 24 * time.Hour

// Pause pauses syncing and automatic backups until the given time (unix s).
// A manual "Back up now" still works.
func (a *App) Pause(until int64) error {
	t := time.Unix(until, 0)
	switch {
	case !t.After(time.Now()):
		return errors.New("pick a time in the future")
	case t.After(time.Now().Add(maxPause)):
		return errors.New("a pause can last a week at most")
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.PausedUntil = t }); err != nil {
		return err
	}
	scheduleResume(t)
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		defer cancel()
		if err := syncPause(ctx, c); err != nil {
			logx.Printf("pause: %v", err)
		}
	}
	logx.Printf("paused syncing and backups until %s", t.Format("Mon 15:04"))
	a.refreshTray()
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// Resume ends a pause now.
func (a *App) Resume() error {
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.PausedUntil = time.Time{} }); err != nil {
		return err
	}
	a.afterResume()
	return nil
}

// afterResume unpauses the folders and catches up on what the pause held back.
func (a *App) afterResume() {
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		if err := syncPause(ctx, c); err != nil {
			logx.Printf("resume: %v", err)
		}
		cancel()
	}
	_ = tasks.Delete(tasks.ResumeTask)
	logx.Printf("syncing and backups resumed")
	a.refreshTray()
	runtime.EventsEmit(a.ctx, "changed")
	go a.runAutoAdd()
}

// pauseLoop ends an expired pause while the window or tray is open, and
// pauses folders that appeared (or a Syncthing that started) mid-pause.
func (a *App) pauseLoop(ctx context.Context) {
	for ctx.Err() == nil {
		s := store.LoadSettings()
		held := len(store.LoadState().PausedFolders) > 0
		switch {
		case s.Paused():
			if c, err := a.client(); err == nil {
				cctx, cancel := a.callCtx()
				_ = syncPause(cctx, c)
				cancel()
			}
		case !s.PausedUntil.IsZero(): // the pause just ran out
			_, _ = store.UpdateSettings(func(s *store.Settings) { s.PausedUntil = time.Time{} })
			a.afterResume()
			runtime.EventsEmit(a.ctx, "toast", "Syncing and backups resumed")
		case held: // Syncthing wasn't reachable when it ran out: try again
			if c, err := a.client(); err == nil {
				cctx, cancel := a.callCtx()
				_ = syncPause(cctx, c)
				cancel()
			}
		}
		sleep(ctx, 30*time.Second)
	}
}

// syncPause makes Syncthing match the pause setting: while paused, every
// running folder is paused and remembered; afterwards only those are resumed,
// so folders paused by hand in Syncthing stay paused.
func syncPause(ctx context.Context, c *syncthing.Client) error {
	paused := store.LoadSettings().Paused()
	held := store.LoadState().PausedFolders
	if !paused && len(held) == 0 {
		return nil
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return err
	}
	var errs []error
	if paused {
		var now []string
		for _, f := range fs {
			if f.Paused {
				continue
			}
			if err := c.PatchFolder(ctx, f.ID, map[string]any{"paused": true}); err != nil {
				errs = append(errs, err)
				continue
			}
			now = append(now, f.ID)
		}
		if len(now) > 0 {
			store.UpdateState(func(st *store.State) {
				for _, id := range now {
					if !slices.Contains(st.PausedFolders, id) {
						st.PausedFolders = append(st.PausedFolders, id)
					}
				}
			})
		}
		return errors.Join(errs...)
	}
	exists := map[string]bool{}
	for _, f := range fs {
		exists[f.ID] = true
	}
	var left []string
	for _, id := range held {
		if !exists[id] {
			continue // removed meanwhile
		}
		if err := c.PatchFolder(ctx, id, map[string]any{"paused": false}); err != nil {
			errs = append(errs, err)
			left = append(left, id)
		}
	}
	store.UpdateState(func(st *store.State) { st.PausedFolders = left })
	return errors.Join(errs...)
}

// runResume is what the one-off \Syncer-Resume task runs.
func runResume() {
	s := store.LoadSettings()
	if s.Paused() {
		return // the pause was extended; its own task will come
	}
	if !s.PausedUntil.IsZero() {
		_, _ = store.UpdateSettings(func(s *store.Settings) { s.PausedUntil = time.Time{} })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if c, err := syncthing.New(); err == nil {
		if err := syncPause(ctx, c); err != nil {
			logx.Printf("resume: %v", err)
		}
	}
	_ = tasks.Delete(tasks.ResumeTask)
	logx.Printf("pause over: syncing and backups resumed")
	// Catch up: apply other PCs' changes and run the backup the pause held back.
	if tasks.Exists(tasks.BackupTask) {
		if err := tasks.Run(tasks.BackupTask); err != nil {
			logx.Printf("resume: %v", err)
		}
	}
}

// scheduleResume registers the task that ends the pause at t.
func scheduleResume(t time.Time) {
	exe, ok := taskExe()
	if !ok {
		return
	}
	err := tasks.Register(tasks.Spec{
		Name:        tasks.ResumeTask,
		Description: "Syncer: ends a pause of game save syncing and backups.",
		Exe:         exe,
		Args:        "--resume",
		At:          t,
		TimeLimit:   10 * time.Minute,
	})
	if err != nil {
		logx.Printf("schedule resume: %v", err)
	}
}

// taskExe is the exe scheduled tasks should run; false for `wails dev`
// builds, which must never be registered.
func taskExe() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return exe, !strings.HasSuffix(strings.ToLower(exe), "-dev.exe")
}
