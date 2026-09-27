package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// A game session starts when a game (an exe in a game's install folder) is
// in the foreground, and lasts until that exe exits: switching to another
// window doesn't end it, and tools that are "games" but never come to the
// foreground (Wallpaper Engine) never start one. While a game runs, syncing
// is held, so a save arriving from another PC never replaces files under
// the running game (which would then overwrite them, or mix two PCs' saves).
// When it exits, syncing resumes (two PCs' changes to the same save become a
// conflict to settle), the saves are backed up, and newer saves on other PCs
// are looked for.

const (
	sessionEvery     = 15 * time.Second
	backupAfterGame  = 2 * time.Minute // let the game finish writing
	sessionStartWait = 30 * time.Second
)

// sessionTracker follows the game exes seen in the foreground.
type sessionTracker struct {
	exes map[string]string // lower-case path -> path
}

// step updates the session with the current foreground exe. isGame tells
// game exes apart; running lists running exes (asked only during a session).
func (t *sessionTracker) step(fg string, isGame func(string) bool, running func() []string) (started, ended bool) {
	was := len(t.exes) > 0
	if fg != "" && isGame(fg) {
		if t.exes == nil {
			t.exes = map[string]string{}
		}
		t.exes[strings.ToLower(filepath.Clean(fg))] = fg
	}
	if len(t.exes) > 0 {
		alive := map[string]bool{}
		for _, p := range running() {
			alive[strings.ToLower(filepath.Clean(p))] = true
		}
		for k := range t.exes {
			if !alive[k] {
				delete(t.exes, k)
			}
		}
	}
	now := len(t.exes) > 0
	return !was && now, was && !now
}

// active reports whether a game session is on.
func (t *sessionTracker) active() bool { return len(t.exes) > 0 }

// games names the exes of the session.
func (t *sessionTracker) games() []string {
	var out []string
	for _, p := range t.exes {
		out = append(out, filepath.Base(p))
	}
	slices.Sort(out)
	return out
}

func (a *App) sessionLoop(ctx context.Context) {
	sleep(ctx, sessionStartWait)
	holdSync(ctx, false) // Syncer quit during a game last time
	for tick := 0; ctx.Err() == nil; tick++ {
		inst := cachedInstalled()
		isGame := func(p string) bool { return inst.Running([]string{p}) }
		a.mu.Lock()
		started, ended := a.session.step(winx.ForegroundPath(), isGame, winx.ProcessPaths)
		games, on := a.session.games(), a.session.active()
		a.mu.Unlock()
		if on && tick%4 == 0 && len(store.LoadState().GamePaused) > 0 {
			// Tells the background task that syncing is held on purpose.
			store.UpdateState(func(st *store.State) { st.GameHeld = time.Now() })
		}
		switch {
		case started:
			logx.Printf("game started: %s", strings.Join(games, ", "))
			if !store.LoadSettings().NoHoldWhilePlaying {
				holdSync(ctx, true)
			}
			runtime.EventsEmit(a.ctx, "changed")
		case ended:
			logx.Printf("game exited")
			holdSync(ctx, false)
			runtime.EventsEmit(a.ctx, "changed")
			go a.afterGame(ctx)
		}
		sleep(ctx, sessionEvery)
	}
}

// afterGame backs up the saves a game just wrote, then looks for newer saves
// on other PCs.
func (a *App) afterGame(ctx context.Context) {
	sleep(ctx, backupAfterGame)
	if ctx.Err() != nil || a.gameRunning() {
		return
	}
	if s := store.LoadSettings(); s.BackupEnabled && !s.Paused() {
		if err := a.BackupNow(); err != nil && !errors.Is(err, backup.ErrBusy) {
			logx.Printf("backup after game: %v", err)
		}
	}
	a.checkNewer(ctx)
}

// gameRunning reports whether a game session is on, or a game is in the
// foreground right now.
func (a *App) gameRunning() bool {
	a.mu.Lock()
	on := a.session.active()
	a.mu.Unlock()
	return on || playing(cachedInstalled())
}

// holdSync pauses every running game folder in Syncthing (on), or resumes
// the ones it paused. Folders paused by hand, or by a pause the user set,
// are left alone; a folder whose game exits during such a pause stays paused
// until that ends.
func holdSync(ctx context.Context, on bool) {
	c, err := syncthing.New()
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := holdSyncWith(cctx, c, on); err != nil {
		logx.Printf("hold sync while playing: %v", err)
	}
}

func holdSyncWith(ctx context.Context, c *syncthing.Client, on bool) error {
	if !on {
		held := store.LoadState().GamePaused
		if len(held) == 0 {
			return nil
		}
		if store.LoadSettings().Paused() {
			// A pause started meanwhile: it resumes them when it ends.
			store.UpdateState(func(st *store.State) {
				for _, id := range st.GamePaused {
					if !slices.Contains(st.PausedFolders, id) {
						st.PausedFolders = append(st.PausedFolders, id)
					}
				}
				st.GamePaused = nil
			})
			return nil
		}
		var errs []error
		var left, modHeld []string
		s := store.LoadSettings()
		for _, id := range held {
			// A mod folder waits while its mod manager is open (see
			// modsTick), and a PC receiving deployed mods keeps its folder
			// paused.
			if isMod(s, id) && (!holdable(s, id) || mods.ManagerRunning(processPaths())) {
				if holdable(s, id) {
					modHeld = append(modHeld, id)
				}
				continue
			}
			if err := c.PatchFolder(ctx, id, map[string]any{"paused": false}); err != nil && exists(ctx, c, id) {
				errs = append(errs, err)
				left = append(left, id)
			}
		}
		store.UpdateState(func(st *store.State) {
			st.GamePaused = left
			for _, id := range modHeld {
				if !slices.Contains(st.ModHeld, id) {
					st.ModHeld = append(st.ModHeld, id)
				}
			}
		})
		return errors.Join(errs...)
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return err
	}
	// Folders a pause the user set holds count too: if that pause ends
	// first, they must stay paused until the game exits.
	userPaused := map[string]bool{}
	for _, id := range store.LoadState().PausedFolders {
		userPaused[id] = true
	}
	var now []string
	var errs []error
	for _, f := range fs {
		if f.ID == meta.FolderID || (f.Paused && !userPaused[f.ID]) {
			continue // paused by hand: not Syncer's to resume
		}
		if f.Paused {
			now = append(now, f.ID)
			continue
		}
		if err := c.PatchFolder(ctx, f.ID, map[string]any{"paused": true}); err != nil {
			errs = append(errs, err)
			continue
		}
		now = append(now, f.ID)
	}
	store.UpdateState(func(st *store.State) {
		for _, id := range now {
			if !slices.Contains(st.GamePaused, id) {
				st.GamePaused = append(st.GamePaused, id)
			}
		}
		st.GameHeld = time.Now()
	})
	return errors.Join(errs...)
}

// exists reports whether Syncthing (still) has folder id.
func exists(ctx context.Context, c *syncthing.Client, id string) bool {
	fs, err := c.Folders(ctx)
	if err != nil {
		return true // can't tell: try again later
	}
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

// releaseStaleHold resumes folders held for a game when the window that held
// them is gone (it quit or crashed during the game): nothing else would.
func releaseStaleHold(ctx context.Context, c *syncthing.Client) {
	st := store.LoadState()
	if len(st.GamePaused) == 0 || time.Since(st.GameHeld) < staleHold {
		return
	}
	if err := holdSyncWith(ctx, c, false); err != nil {
		logx.Printf("resume folders held for a game: %v", err)
	} else {
		logx.Printf("resumed folders held for a game (Syncer wasn't running when it ended)")
	}
}

// staleHold: a hold not confirmed for this long has no window behind it.
const staleHold = 5 * time.Minute
