package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// Mod folders are a mod manager's folders (Vortex's staging folder with the
// installed mods, and its profiles with the load orders), found on this PC
// when "Find installed mods" is on. They sync like save folders, with a few
// differences:
//   - they are added by hand (or, experimentally, automatically), never
//     just because a game was found;
//   - they are left out of the Drive backup unless it's turned on for them;
//   - they are paused while the mod manager is open, so it never sees a
//     half-arrived mod, and resumed when it closes;
//   - other PCs find them through their own mod manager, not by path.

// processPaths lists running executables (a variable for tests).
var processPaths = winx.ProcessPaths

// modFounds lists this PC's mod folders as found games for the UI, when
// "Find installed mods" is on. Deployed mods are only listed with the
// experimental option on.
func modFounds(s store.Settings) []discover.Found {
	if !s.FindMods {
		return nil
	}
	var out []discover.Found
	for _, f := range mods.Detect() {
		if f.Kind == mods.KindDeployed && !s.SyncDeployedMods {
			continue
		}
		size, files, mod := modSize(f)
		out = append(out, discover.Found{Name: f.Label(), Path: f.Path, Known: true, Size: size, Files: files,
			Modified: mod, Kind: f.Kind, Manager: f.Manager, ModGame: f.Game, ModKey: f.Key(), Warn: f.Warn})
	}
	return out
}

// modSize measures a mod folder: a deployed folder counts only the files
// its mod manager deployed.
func modSize(f mods.Found) (int64, int, time.Time) {
	if f.Kind == mods.KindDeployed && f.Deploy != nil {
		return deployedSize(f)
	}
	return mods.Measure(f.Path)
}

// AddModFolder starts syncing one of this PC's mod folders, identified by
// its key (the UI never passes a path: the folder is found again here).
func (a *App) AddModFolder(key string) error {
	s := store.LoadSettings()
	if !s.FindMods {
		return errors.New(`turn on "Find installed mods" in Settings first`)
	}
	mods.Forget()
	f, ok := mods.Find(key)
	if !ok {
		return errors.New("that mod folder is no longer on this PC")
	}
	if f.Kind == mods.KindDeployed {
		return a.addDeployedSource(f)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	id, err := addModFolder(ctx, c, f)
	if err != nil {
		return err
	}
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c)
	modsTick(ctx, c)
	logx.Printf("added mod folder %s (%s) as %s", f.Label(), f.Path, id)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// addModFolder creates a synced folder for a mod folder and returns its id.
func addModFolder(ctx context.Context, c *syncthing.Client, f mods.Found) (string, error) {
	if err := mods.CheckModSyncable(f.Kind, f.Path); err != nil {
		return "", err
	}
	st, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	synced, err := syncthingFolders(ctx, c)
	if err != nil {
		return "", err
	}
	taken, backupOnly, err := checkNewFolderWith(f.Path, synced, func(string) error { return nil })
	if err != nil {
		return "", err
	}
	if backupOnly != "" {
		return "", coveredError("already backed up only")
	}
	taken[meta.FolderID] = true
	label := f.Label()
	id := syncID(st.MyID, label, f.Path, taken)
	sf := meta.SharedFolder{ID: id, Label: label, Root: f.Root, Rel: f.Rel, Kind: f.Kind, ModGame: f.Game}
	if err := meta.RegisterMod(id, f.GameName, sf); err != nil {
		return "", err
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		delete(s.Ignored, id)
		delete(s.Dismissed, dismissKey(f.Path))
		if f.Kind == mods.KindProfiles {
			// Each PC keeps its own game settings (graphics, resolution).
			if k := dismissKey(f.Path); len(s.Exclude[k]) == 0 {
				s.Exclude[k] = []string{"*.ini"}
			}
		}
	}); err != nil {
		return "", err
	}
	if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(id, label, f.Path, f.Kind, false), st.MyID, peerIDs(ctx, c, st.MyID)); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, id) })
		return "", err
	}
	mods.Forget()
	return id, nil
}

// syncthingFolders lists Syncthing's folders other than Syncer's own.
func syncthingFolders(ctx context.Context, c *syncthing.Client) ([]backup.Folder, error) {
	fs, err := c.Folders(ctx)
	if err != nil {
		return nil, err
	}
	var out []backup.Folder
	for _, f := range fs {
		if f.ID != meta.FolderID {
			out = append(out, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
		}
	}
	return out, nil
}

// peerIDs lists the paired PCs.
func peerIDs(ctx context.Context, c *syncthing.Client, me string) []string {
	ds, _ := c.Devices(ctx)
	var out []string
	for _, d := range ds {
		if d.DeviceID != me {
			out = append(out, d.DeviceID)
		}
	}
	return out
}

// forgetMod drops what Syncer keeps about a mod folder that is gone.
func forgetMod(s *store.Settings, id string) {
	delete(s.Mods, id)
	delete(s.NoBackup, id)
}

// isMod reports whether a folder id is a mod folder.
func isMod(s store.Settings, id string) bool {
	_, ok := s.Mods[id]
	return ok
}

// joinModAvailable starts syncing a mod folder another PC offers.
func (a *App) joinModAvailable(ctx context.Context, c *syncthing.Client, v meta.Avail) error {
	if v.Kind == mods.KindDeployed {
		return a.joinDeployed(ctx, c, v)
	}
	if v.Path == "" {
		return errors.New("the mod manager doesn't manage that game on this PC")
	}
	if err := mods.CheckModSyncable(v.Kind, v.Path); err != nil {
		return err
	}
	synced, err := syncthingFolders(ctx, c)
	if err != nil {
		return err
	}
	if err := overlapsSynced(v.Path, v.ID, synced); err != nil {
		return err
	}
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	label := cmpOr(v.Label, v.ID)
	if err := meta.RegisterMod(v.ID, label, v.SharedFolder); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		delete(s.Ignored, v.ID)
		delete(s.Dismissed, dismissKey(v.Path))
	})
	if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(v.ID, label, v.Path, v.Kind, false), st.MyID, peerIDs(ctx, c, st.MyID)); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, v.ID) })
		return err
	}
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c)
	modsTick(ctx, c)
	logx.Printf("added mod folder %s (%s) from another PC", label, v.Path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// resyncMod turns syncing back on for a mod folder that is backed up only.
// The mod manager must still use that folder for the game.
func (a *App) resyncMod(ctx context.Context, c *syncthing.Client, id string, lf store.LocalFolder, mf store.ModFolder) error {
	if mf.Kind == mods.KindDeployed {
		return errors.New(`remove it, then sync it again from "Found on this PC"`)
	}
	p, err := mods.Resolve(mf.Root, mf.Rel)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(p), filepath.Clean(lf.Path)) {
		return errors.New("Vortex keeps this game's mods somewhere else now: remove this one, then sync the new folder from \"Found on this PC\"")
	}
	if err := mods.CheckModSyncable(mf.Kind, p); err != nil {
		return err
	}
	synced, err := syncthingFolders(ctx, c)
	if err != nil {
		return err
	}
	if err := overlapsSynced(p, "", synced); err != nil {
		return err
	}
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	sid := lf.SyncID
	if sid == "" {
		taken := map[string]bool{meta.FolderID: true}
		for _, f := range synced {
			taken[f.ID] = true
		}
		sid = syncID(st.MyID, lf.Label, p, taken)
	}
	backupOff := store.LoadSettings().NoBackup[id]
	if err := meta.RegisterMod(sid, mf.GameName, meta.SharedFolder{ID: sid, Label: lf.Label, Root: mf.Root, Rel: mf.Rel,
		Kind: mf.Kind, ModGame: mf.Game}); err != nil {
		return err
	}
	if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(sid, lf.Label, p, mf.Kind, false), st.MyID, peerIDs(ctx, c, st.MyID)); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, sid) })
		return err
	}
	_ = forgetBackup(id, false) // backups continue under the synced id
	_, err = store.UpdateSettings(func(s *store.Settings) {
		delete(s.BackupOnly, id)
		delete(s.NoBackup, id)
		delete(s.Mods, id)
		delete(s.Ignored, sid)
		delete(s.Dismissed, dismissKey(p))
		if backupOff {
			s.NoBackup[sid] = true
		} else {
			delete(s.NoBackup, sid)
		}
	})
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c)
	modsTick(ctx, c)
	logx.Printf("syncing mod folder %s again", lf.Label)
	runtime.EventsEmit(a.ctx, "changed")
	return err
}

// ---- auto-add (experimental) --------------------------------------------------

// autoAddMods syncs this PC's staging and profiles folders without asking,
// when the experimental option is on. Deployed mods are never added on their
// own. It returns the folders added.
func autoAddMods(ctx context.Context, c *syncthing.Client, s store.Settings) []string {
	if !s.FindMods || !s.AutoAddMods || s.SyncDisabled || s.Paused() {
		return nil
	}
	var added []string
	for _, f := range mods.Detect() {
		if ctx.Err() != nil {
			break
		}
		size, files, _ := mods.Measure(f.Path)
		if !wantAutoMod(f, size, files, s) {
			continue
		}
		if _, err := addModFolder(ctx, c, f); err != nil {
			var ce coveredError
			if !errors.As(err, &ce) {
				logx.Printf("auto-add mods %s: %v", f.Label(), err)
			}
			continue
		}
		added = append(added, f.Label())
		logx.Printf("auto-added mod folder %s (%s)", f.Label(), f.Path)
	}
	return added
}

func wantAutoMod(f mods.Found, size int64, files int, s store.Settings) bool {
	switch {
	case f.Kind != mods.KindStaging && f.Kind != mods.KindProfiles:
		return false
	case files == 0:
		return false
	case s.ModsMaxGB > 0 && size > int64(s.ModsMaxGB)<<30:
		return false
	case s.Dismissed[dismissKey(f.Path)]:
		return false
	}
	return true
}

// ---- holding mod folders while the mod manager is open ------------------------

var modsTickMu sync.Mutex

// modsLoop keeps mod folders paused while their mod manager is open.
func (a *App) modsLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if c, err := a.client(); err == nil {
			cctx, cancel := a.callCtx()
			if modsTick(cctx, c) {
				runtime.EventsEmit(a.ctx, "changed")
			}
			cancel()
		}
		sleep(ctx, 30*time.Second)
	}
}

// modsTick pauses every running mod folder while a mod manager is open (it
// could see a half-arrived mod, or change files mid-transfer) and resumes
// the ones it paused when it closes. A pause of all syncing takes
// precedence: while it lasts nothing is resumed here. It reports whether it
// changed anything.
func modsTick(ctx context.Context, c *syncthing.Client) bool {
	modsTickMu.Lock()
	defer modsTickMu.Unlock()
	s := store.LoadSettings()
	held := store.LoadState().ModHeld
	if len(s.Mods) == 0 && len(held) == 0 {
		return false
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return false
	}
	busy := mods.ManagerRunning(processPaths())
	changed := false
	if busy {
		var now []string
		for _, f := range fs {
			if !isMod(s, f.ID) || f.Paused || !holdable(s, f.ID) {
				continue
			}
			if err := c.PatchFolder(ctx, f.ID, map[string]any{"paused": true}); err != nil {
				logx.Printf("mods: pause %s: %v", f.ID, err)
				continue
			}
			now = append(now, f.ID)
		}
		if len(now) > 0 {
			changed = true
			logx.Printf("mod manager open: paused %d mod folder(s)", len(now))
			store.UpdateState(func(st *store.State) {
				for _, id := range now {
					if !slices.Contains(st.ModHeld, id) {
						st.ModHeld = append(st.ModHeld, id)
					}
				}
			})
		}
		return changed
	}
	if s.Paused() || len(held) == 0 {
		return false
	}
	exists := map[string]bool{}
	for _, f := range fs {
		exists[f.ID] = true
	}
	var left []string
	for _, id := range held {
		if !exists[id] || !holdable(s, id) {
			continue
		}
		if err := c.PatchFolder(ctx, id, map[string]any{"paused": false}); err != nil {
			left = append(left, id)
			continue
		}
		changed = true
	}
	store.UpdateState(func(st *store.State) { st.ModHeld = left })
	if changed {
		logx.Printf("mod manager closed: mod folders resumed")
	}
	return changed
}

// holdable reports whether modsTick pauses and resumes a mod folder: a PC
// receiving deployed mods keeps its folder paused between updates, which
// only an update resumes.
func holdable(s store.Settings, id string) bool {
	mf, ok := s.Mods[id]
	return ok && !(mf.Kind == mods.KindDeployed && mf.Role != meta.RoleSource)
}

// modKind is a folder's mod kind ("" for a save folder).
func modKind(s store.Settings, id string) string { return s.Mods[id].Kind }
