package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// Deployed mods (experimental) are the mod files Vortex deployed into a
// game's own folder (e.g. Skyrim's Data), synced so another PC can play with
// them without Vortex. Because that folder also holds the game itself, it
// is handled far more carefully than a save folder:
//
//   - One PC is the source (Syncthing "send only"); the others receive
//     ("receive only"). The newest claim wins.
//   - Only the files in Vortex's deployment are in scope: the .stignore
//     un-ignores exactly those files and ignores everything else, so the
//     game's own files are never scanned, sent, changed or deleted.
//   - The source publishes an inventory of its deployment (sizes, hashes,
//     game version, plugin lists), so a receiver knows exactly what an
//     update changes before anything arrives.
//   - A receiver's folder stays paused. It only runs during an update the
//     user applies, after gates (same game version, Vortex and the game
//     closed, source online and not mid-change, enough disk space, deletions
//     confirmed), a snapshot of every file the update replaces, and an audit
//     of the game's own files. Afterwards everything is checked again; if
//     anything is off the folder is held and can be rolled back.

// ---- local inventory files ------------------------------------------------------

func modStateDir() string {
	d := filepath.Join(paths.AppDir(), "mods")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func invPath(id, kind string) string { return filepath.Join(modStateDir(), id+"."+kind+".json") }

// sourceState is what the source PC keeps about the deployment it sends.
type sourceState struct {
	Inv   mods.Inventory `json:"inv"`
	Stamp string         `json:"stamp"` // what it was built from (see sourceStamp)
	Built time.Time      `json:"built"`
	// Tombstones are files removed from the deployment, kept in scope for a
	// while so their deletion reaches the receivers.
	Tombstones map[string]time.Time `json:"tombstones,omitempty"`
}

const tombstoneAge = 30 * 24 * time.Hour

func loadJSON(p string, v any) bool {
	b, err := os.ReadFile(p)
	return err == nil && json.Unmarshal(b, v) == nil
}

func loadSource(id string) *sourceState {
	var ss sourceState
	if !paths.ValidID(id) || !loadJSON(invPath(id, "source"), &ss) {
		return nil
	}
	return &ss
}

func loadInv(id, kind string) *mods.Inventory {
	var inv mods.Inventory
	if !paths.ValidID(id) || !loadJSON(invPath(id, kind), &inv) {
		return nil
	}
	return &inv
}

func saveJSON(p string, v any) error {
	if v == nil {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return store.WriteJSON(p, v)
}

func forgetModFiles(id string) {
	if !paths.ValidID(id) {
		return
	}
	for _, k := range []string{"source", "applied", "target"} {
		_ = os.Remove(invPath(id, k))
	}
}

// deployedScope are the .stignore lines that limit a deployed-mods folder
// to the mod files.
func deployedScope(id, _ string) []string {
	mf := store.LoadSettings().Mods[id]
	if mf.Role == meta.RoleSource {
		return scopeLines(loadSource(id), nil, nil)
	}
	var target *mods.Inventory
	if ph := store.LoadState().ModSync[id].Phase; ph == phaseApplying || ph == phaseHeld {
		target = loadInv(id, "target") // an update in progress: old and new files
	}
	return scopeLines(nil, loadInv(id, "applied"), target)
}

// scopeLines un-ignores each mod file of a source's deployment (and the
// ones it removed lately, so their deletion reaches the receivers), or of
// the inventories a receiver applied and is applying, then ignores
// everything else. With nothing known, everything is ignored.
func scopeLines(ss *sourceState, invs ...*mods.Inventory) []string {
	seen := map[string]bool{}
	var lines []string
	add := func(rel string) {
		if k := strings.ToLower(rel); !seen[k] && mods.ValidInvRel(rel) {
			seen[k] = true
			lines = append(lines, "!/"+rel)
		}
	}
	if ss != nil {
		invs = append(invs, &ss.Inv)
		for rel, t := range ss.Tombstones {
			if time.Since(t) < tombstoneAge {
				add(rel)
			}
		}
	}
	for _, inv := range invs {
		if inv != nil {
			for _, f := range inv.Files {
				add(f.Rel)
			}
		}
	}
	sort.Strings(lines)
	return append(lines, "*")
}

const (
	phaseIdle     = "idle"
	phasePending  = "pending"
	phaseApplying = "applying"
	phaseHeld     = "held"
	phaseBusy     = "busy"
)

// ---- source ---------------------------------------------------------------------

// addDeployedSource starts sending this PC's deployed mods of a game.
func (a *App) addDeployedSource(f mods.Found) error {
	s := store.LoadSettings()
	if !s.SyncDeployedMods {
		return errors.New(`turn on "Sync deployed mods in the game folder" in Settings first`)
	}
	if len(f.Warn) > 0 && mods.CheckModSyncable(mods.KindDeployed, f.Path) != nil {
		return errors.New(f.Warn[0])
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	synced, err := syncthingFolders(ctx, c)
	if err != nil {
		return err
	}
	taken, backupOnly, err := checkNewFolderWith(f.Path, synced, func(p string) error { return mods.CheckModSyncable(mods.KindDeployed, p) })
	if err != nil {
		return err
	}
	if backupOnly != "" {
		return coveredError("already backed up only")
	}
	taken[meta.FolderID] = true
	id := syncID(st.MyID, f.Label(), f.Path, taken)
	if err := a.becomeSource(ctx, c, st.MyID, id, f.Label(), f.Game, f.GameName, f.Root, f.Rel, f.Path, f.GameDir, false); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// MakeModSource makes this PC the one sending a deployed-mods folder it
// receives now: its own Vortex must deploy that game here.
func (a *App) MakeModSource(id string) error {
	s := store.LoadSettings()
	mf, ok := s.Mods[id]
	if !ok || mf.Kind != mods.KindDeployed {
		return errors.New("not a deployed-mods folder")
	}
	if mf.Role == meta.RoleSource {
		return nil
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return err
	}
	if !mods.DeploysHere(f.Path) {
		return errors.New("Vortex doesn't deploy this game on this PC, so there's nothing to send from here")
	}
	if err := a.becomeSource(ctx, c, st.MyID, id, cmpOr(f.Label, id), mf.Game, mf.GameName, mf.Root, mf.Rel, f.Path, mods.GameDir(mf.Game), true); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// becomeSource builds the inventory of the deployment at path and starts
// sending it (a new folder, or an existing one this PC received).
func (a *App) becomeSource(ctx context.Context, c *syncthing.Client, me, id, label, game, gameName, root, rel, path, gameDir string, existing bool) error {
	procs := processPaths()
	if mods.ManagerRunning(procs) {
		return errors.New("close Vortex first")
	}
	if gameRunning(gameDir, procs) {
		return errors.New("close the game first")
	}
	inv, err := mods.BuildInventory(game, path, gameDir)
	if err != nil {
		return err
	}
	if len(inv.Files) == 0 {
		return errors.New("Vortex hasn't deployed any mods for this game here")
	}
	inv.Folder, inv.Gen = id, 1
	if src, ok := meta.ModSource(id); ok {
		if old, ok := meta.ModInventories(src.Device)[id]; ok {
			inv.Gen = old.Gen + 1
		}
	}
	if prev := loadInv(id, "applied"); prev != nil && prev.Gen >= inv.Gen {
		inv.Gen = prev.Gen + 1
	}
	ss := &sourceState{Inv: inv, Stamp: sourceStamp(game, path), Built: time.Now()}
	if err := saveJSON(invPath(id, "source"), ss); err != nil {
		return err
	}
	since := time.Now().UnixNano()
	sf := meta.SharedFolder{ID: id, Label: label, Root: root, Rel: rel, Kind: mods.KindDeployed, ModGame: game}
	if err := meta.RegisterMod(id, gameName, sf); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[id]
		mf.Role = meta.RoleSource
		s.Mods[id] = mf
		delete(s.Ignored, id)
		delete(s.Dismissed, dismissKey(path))
	})
	store.UpdateState(func(st *store.State) {
		if st.ModSync == nil {
			st.ModSync = map[string]store.ModSyncState{}
		}
		st.ModSync[id] = store.ModSyncState{Phase: phaseIdle, SourceGen: inv.Gen, SourceHash: inv.Hash, Since: since}
	})
	publishInventories(me)
	if existing {
		if err := applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, path)); err != nil {
			return err
		}
		if err := c.PatchFolder(ctx, id, map[string]any{"type": "sendonly", "paused": false}); err != nil {
			return err
		}
	} else if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(id, label, path, mods.KindDeployed, true), me, peerIDs(ctx, c, me)); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, id) })
		store.UpdateState(func(st *store.State) { delete(st.ModSync, id) })
		forgetModFiles(id)
		publishInventories(me)
		return err
	}
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: label, Phase: "source", Gen: inv.Gen,
		Summary: fmt.Sprintf("This PC sends %d deployed mod files (version %d)", len(inv.Files), inv.Gen),
		Checks: []mods.Check{{Name: "Deployment read", OK: true, Detail: fmt.Sprintf("%d files, %d skipped", len(inv.Files), len(inv.Skipped))},
			{Name: "Game version recorded", OK: inv.Version != (mods.Version{}), Warn: inv.Version == (mods.Version{})}}})
	logx.Printf("sending deployed mods %s (%s) as %s, version %d", label, path, id, inv.Gen)
	return nil
}

// sourceStamp changes when the deployment or the plugin lists change.
func sourceStamp(game, path string) string {
	st := mods.ManifestStamp(path)
	for _, name := range []string{"plugins.txt", "loadorder.txt"} {
		if p := mods.PluginListPath(game, name); p != "" {
			if fi, err := os.Stat(p); err == nil {
				st += fmt.Sprintf("%s|%d|%d;", name, fi.Size(), fi.ModTime().UnixNano())
			}
		}
	}
	return st
}

// publishInventories writes this PC's inventories (of the folders it sends)
// into the shared metadata.
func publishInventories(me string) {
	if me == "" {
		return
	}
	s := store.LoadSettings()
	invs := map[string]mods.Inventory{}
	for id, mf := range s.Mods {
		if mf.Kind != mods.KindDeployed || mf.Role != meta.RoleSource {
			continue
		}
		if ss := loadSource(id); ss != nil {
			invs[id] = ss.Inv
		}
	}
	old := meta.ModInventories(me)
	same := len(old) == len(invs)
	for id, inv := range invs {
		if o, ok := old[id]; !ok || o.Hash != inv.Hash || o.Gen != inv.Gen || o.Busy != inv.Busy {
			same = false
		}
	}
	if same {
		return
	}
	if err := meta.WriteModInventories(me, invs); err != nil {
		logx.Printf("mods: publish inventories: %v", err)
	}
}

// gameRunning reports whether a program in the game's folder is running.
func gameRunning(gameDir string, procs []string) bool {
	if gameDir == "" {
		return false
	}
	for _, p := range procs {
		if filepath.IsAbs(p) && paths.Within(gameDir, p) {
			return true
		}
	}
	return false
}

// ---- keeping deployed folders in line (every tick) ---------------------------

// deployedTick runs from modsTick. The source rebuilds its inventory when
// Vortex deployed something new and hands over to a newer source; a
// receiver keeps its folder paused and notes when an update is waiting.
func deployedTick(ctx context.Context, c *syncthing.Client, s store.Settings, fs []syncthing.Folder, procs []string) bool {
	var me string
	changed := false
	byID := map[string]syncthing.Folder{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	for id, mf := range s.Mods {
		f, ok := byID[id]
		if mf.Kind != mods.KindDeployed || !ok {
			continue
		}
		if me == "" {
			st, err := c.Status(ctx)
			if err != nil {
				return changed
			}
			me = st.MyID
		}
		if mf.Role == meta.RoleSource {
			changed = sourceTick(ctx, c, me, id, mf, f, procs) || changed
		} else {
			changed = receiverTick(ctx, c, me, id, f) || changed
		}
	}
	if me != "" {
		publishInventories(me)
	}
	return changed
}

func sourceTick(ctx context.Context, c *syncthing.Client, me, id string, mf store.ModFolder, f syncthing.Folder, procs []string) bool {
	ms := store.LoadState().ModSync[id]
	// Another PC claimed the source role later: this one receives now.
	if src, ok := meta.ModSource(id); ok && src.Device != me && src.Since > ms.Since {
		demoteSource(ctx, c, id, f, src)
		return true
	}
	ss := loadSource(id)
	if ss == nil {
		holdMods(ctx, c, id, "this PC's list of deployed files is missing: make it the source again")
		return true
	}
	gameDir := mods.GameDir(mf.Game)
	busy := mods.ManagerRunning(procs) || gameRunning(gameDir, procs)
	if busy != ss.Inv.Busy {
		ss.Inv.Busy = busy
		_ = saveJSON(invPath(id, "source"), ss)
		if !busy {
			ss.Built = time.Time{} // rebuild now: Vortex may have deployed
		}
	}
	if busy {
		return false
	}
	stamp := sourceStamp(mf.Game, f.Path)
	if stamp == ss.Stamp && time.Since(ss.Built) < 30*time.Minute {
		return false
	}
	inv, err := mods.BuildInventory(mf.Game, f.Path, gameDir)
	if err != nil {
		ss.Inv.Busy = true // receivers wait until it's readable again
		_ = saveJSON(invPath(id, "source"), ss)
		holdMods(ctx, c, id, "can't read Vortex's deployment: "+err.Error())
		return true
	}
	ss.Stamp, ss.Built = stamp, time.Now()
	if ms.Phase == phaseHeld { // readable again: send again
		resumeSource(ctx, c, id)
	}
	if inv.Hash == ss.Inv.Hash {
		_ = saveJSON(invPath(id, "source"), ss)
		return ms.Phase == phaseHeld
	}
	if ss.Tombstones == nil {
		ss.Tombstones = map[string]time.Time{}
	}
	now := mods.Scope(&inv)
	for _, old := range ss.Inv.Files {
		if !now[strings.ToLower(old.Rel)] {
			ss.Tombstones[old.Rel] = time.Now()
		}
	}
	for rel, t := range ss.Tombstones {
		if now[strings.ToLower(rel)] || time.Since(t) > tombstoneAge {
			delete(ss.Tombstones, rel)
		}
	}
	inv.Folder, inv.Gen = id, ss.Inv.Gen+1
	ss.Inv = inv
	if err := saveJSON(invPath(id, "source"), ss); err != nil {
		return false
	}
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		m.SourceGen, m.SourceHash, m.Phase, m.Held = inv.Gen, inv.Hash, phaseIdle, ""
		st.ModSync[id] = m
	})
	if err := applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, f.Path)); err != nil {
		logx.Printf("mods: scope of %s: %v", id, err)
	}
	_ = c.Rescan(ctx, id)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "source", Gen: inv.Gen,
		Summary: fmt.Sprintf("Vortex's deployment changed: version %d, %d files", inv.Gen, len(inv.Files)),
		Checks:  []mods.Check{{Name: "Deployment read", OK: true, Detail: fmt.Sprintf("%d files, %d skipped", len(inv.Files), len(inv.Skipped))}}})
	logx.Printf("deployed mods %s: version %d (%d files)", cmpOr(f.Label, id), inv.Gen, len(inv.Files))
	return true
}

// resumeSource lets a source folder that was held send again.
func resumeSource(ctx context.Context, c *syncthing.Client, id string) {
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		m.Phase, m.Held = phaseIdle, ""
		st.ModSync[id] = m
	})
	if !store.LoadSettings().Paused() && !slices.Contains(store.LoadState().ModHeld, id) {
		_ = c.PatchFolder(ctx, id, map[string]any{"paused": false})
	}
}

// dropModState forgets what Syncer kept about a mod folder that no longer
// syncs under id (removed, or now backed up only), and stops publishing its
// inventory.
func dropModState(ctx context.Context, c *syncthing.Client, id string) {
	store.UpdateState(func(st *store.State) {
		delete(st.ModSync, id)
		st.ModHeld = slices.DeleteFunc(st.ModHeld, func(h string) bool { return h == id })
	})
	forgetModFiles(id)
	mods.PruneSnapshots(id, 1, 14*24*time.Hour)
	if c != nil {
		if st, err := c.Status(ctx); err == nil {
			publishInventories(st.MyID)
		}
	}
}

// demoteSource makes this PC a receiver of a folder another PC now sends.
// What it deployed itself counts as applied, so the first update removes
// what the new source doesn't have.
func demoteSource(ctx context.Context, c *syncthing.Client, id string, f syncthing.Folder, src meta.Source) {
	if err := c.PatchFolder(ctx, id, map[string]any{"type": "receiveonly", "paused": true}); err != nil {
		logx.Printf("mods: hand %s over to %s: %v", id, src.Name, err)
		return
	}
	if ss := loadSource(id); ss != nil {
		inv := ss.Inv
		inv.Busy = false
		_ = saveJSON(invPath(id, "applied"), &inv)
	}
	_ = saveJSON(invPath(id, "source"), nil)
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[id]
		mf.Role = meta.RoleReceiver
		s.Mods[id] = mf
	})
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		st.ModSync[id] = store.ModSyncState{Phase: phasePending, AppliedGen: m.SourceGen}
	})
	_ = applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, f.Path))
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "source",
		Summary: fmt.Sprintf("%s sends these mods now; this PC receives them", src.Name),
		Checks:  []mods.Check{{Name: "Handed over", OK: true, Detail: src.Name}}})
	logx.Printf("deployed mods %s: %s is the source now, this PC receives", cmpOr(f.Label, id), src.Name)
}

func receiverTick(ctx context.Context, c *syncthing.Client, me, id string, f syncthing.Folder) bool {
	ms := store.LoadState().ModSync[id]
	if ms.Phase == phaseApplying {
		if applying.has(id) || time.Since(ms.Started) < 3*time.Hour {
			return false
		}
		holdMods(ctx, c, id, "an update was interrupted: roll back, or apply it again")
		return true
	}
	changed := false
	if !f.Paused { // a receiver rests paused
		if err := c.PatchFolder(ctx, id, map[string]any{"paused": true}); err == nil {
			changed = true
		}
	}
	if ms.Phase == phaseHeld {
		return changed
	}
	next := phaseIdle
	if src, ok := meta.ModSource(id); ok && src.Device != me {
		if inv, ok := meta.ModInventories(src.Device)[id]; ok && inv.Gen > ms.AppliedGen {
			next = phasePending
		}
	}
	if next != ms.Phase {
		store.UpdateState(func(st *store.State) {
			m := st.ModSync[id]
			m.Phase = next
			if st.ModSync == nil {
				st.ModSync = map[string]store.ModSyncState{}
			}
			st.ModSync[id] = m
		})
		changed = true
	}
	return changed
}

// holdMods pauses a deployed-mods folder and holds its updates, saying why.
func holdMods(ctx context.Context, c *syncthing.Client, id, why string) {
	if c != nil {
		_ = c.PatchFolder(ctx, id, map[string]any{"paused": true})
	}
	store.UpdateState(func(st *store.State) {
		if st.ModSync == nil {
			st.ModSync = map[string]store.ModSyncState{}
		}
		m := st.ModSync[id]
		if m.Phase == phaseHeld && m.Held == why {
			return
		}
		m.Phase, m.Held = phaseHeld, why
		st.ModSync[id] = m
		logx.Printf("deployed mods %s held: %s", id, why)
	})
}

// holdAllDeployed pauses every deployed-mods folder and holds its updates.
func (a *App) holdAllDeployed(why string) {
	c, err := a.client()
	if err != nil {
		c = nil
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	for id, mf := range store.LoadSettings().Mods {
		if mf.Kind == mods.KindDeployed {
			holdMods(ctx, c, id, why)
		}
	}
	runtime.EventsEmit(a.ctx, "changed")
}

// ---- receiving ------------------------------------------------------------------

// joinDeployed starts receiving deployed mods another PC sends. Nothing is
// changed yet: the folder is added paused, and the user applies the update.
func (a *App) joinDeployed(ctx context.Context, c *syncthing.Client, v meta.Avail) error {
	s := store.LoadSettings()
	if !s.SyncDeployedMods {
		return errors.New(`turn on "Sync deployed mods in the game folder" in Settings first`)
	}
	if v.Path == "" {
		return errors.New("that game isn't installed on this PC")
	}
	if mods.DeploysHere(v.Path) {
		return errors.New("Vortex deploys this game's mods on this PC itself; receiving another PC's deployment would mix the two")
	}
	if err := mods.CheckModSyncable(mods.KindDeployed, v.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	if err := meta.RegisterMod(v.ID, strings.TrimSuffix(label, " (deployed mods)"), v.SharedFolder); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[v.ID]
		mf.Role = meta.RoleReceiver
		s.Mods[v.ID] = mf
		delete(s.Ignored, v.ID)
		delete(s.Dismissed, dismissKey(v.Path))
	})
	store.UpdateState(func(st *store.State) {
		if st.ModSync == nil {
			st.ModSync = map[string]store.ModSyncState{}
		}
		st.ModSync[v.ID] = store.ModSyncState{Phase: phasePending}
	})
	if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(v.ID, label, v.Path, mods.KindDeployed, false), st.MyID, peerIDs(ctx, c, st.MyID)); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, v.ID) })
		store.UpdateState(func(st *store.State) { delete(st.ModSync, v.ID) })
		return err
	}
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("receiving deployed mods %s (%s) from %s; nothing applied yet", label, v.Path, v.From)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// ModPreview is what applying the waiting update would do, and whether it can.
type ModPreview struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	From           string       `json:"from"` // the source PC
	Gen            int64        `json:"gen"`
	AppliedGen     int64        `json:"appliedGen"`
	Added          int          `json:"added"`
	Changed        int          `json:"changed"`
	Removed        int          `json:"removed"`
	Same           int          `json:"same"`
	Bytes          int64        `json:"bytes"`
	RemovedPlugins []string     `json:"removedPlugins"`
	PluginLists    []string     `json:"pluginLists"` // plugin lists it writes
	NeedConfirm    bool         `json:"needConfirm"` // it removes a lot, or plugins
	Checks         []mods.Check `json:"checks"`
	Ready          bool         `json:"ready"`
}

// applyPlan is a checked update, ready to apply.
type applyPlan struct {
	id, label, game, path, gameDir string
	folder                         syncthing.Folder
	src                            meta.Source
	inv                            mods.Inventory
	prev                           *mods.Inventory
	diff                           mods.Diff
	preview                        ModPreview
}

// diskFree is the free space for this user on path's drive (a variable for tests).
var diskFree = winx.DiskFree

// planUpdate checks whether the waiting update of id can be applied.
func planUpdate(ctx context.Context, c *syncthing.Client, id string, confirmDeletes bool) (*applyPlan, error) {
	s := store.LoadSettings()
	mf, ok := s.Mods[id]
	if !ok || mf.Kind != mods.KindDeployed {
		return nil, errors.New("not a deployed-mods folder")
	}
	if mf.Role == meta.RoleSource {
		return nil, errors.New("this PC sends these mods")
	}
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return nil, err
	}
	src, ok := meta.ModSource(id)
	if !ok || src.Device == st.MyID {
		return nil, errors.New("no other PC sends these mods right now")
	}
	inv, ok := meta.ModInventories(src.Device)[id]
	if !ok {
		return nil, fmt.Errorf("%s's list of deployed files hasn't arrived yet", src.Name)
	}
	p := &applyPlan{id: id, label: cmpOr(f.Label, id), game: mf.Game, path: f.Path, folder: f, src: src, inv: inv,
		prev: loadInv(id, "applied"), gameDir: mods.GameDir(mf.Game)}
	p.diff = mods.DiffLocal(f.Path, p.prev, inv)
	appliedGen := store.LoadState().ModSync[id].AppliedGen
	pv := ModPreview{ID: id, Label: p.label, From: src.Name, Gen: inv.Gen, AppliedGen: appliedGen,
		Added: len(p.diff.Added), Changed: len(p.diff.Changed), Removed: len(p.diff.Removed), Same: p.diff.Same,
		Bytes: p.diff.Bytes, RemovedPlugins: p.diff.RemovedPlugins}
	for n := range inv.PluginLists {
		pv.PluginLists = append(pv.PluginLists, n)
	}
	sort.Strings(pv.PluginLists)
	prevFiles := 0
	if p.prev != nil {
		prevFiles = len(p.prev.Files)
	}
	pv.NeedConfirm = len(p.diff.RemovedPlugins) > 0 || prevFiles >= 8 && len(p.diff.Removed)*4 > prevFiles

	procs := processPaths()
	check := func(name string, ok bool, detail string) {
		pv.Checks = append(pv.Checks, mods.Check{Name: name, OK: ok, Detail: detail})
	}
	check("Experimental option is on", s.SyncDeployedMods, "")
	check("Syncing isn't paused", !s.Paused(), "")
	check("Vortex is closed", !mods.ManagerRunning(procs), "")
	check("The game isn't running", !gameRunning(p.gameDir, procs), "")
	check("The game is installed here", p.gameDir != "" && paths.Within(p.gameDir, f.Path), p.gameDir)
	check("Vortex doesn't deploy this game here", !mods.DeploysHere(f.Path), "")
	check("The folder only receives", f.Type == "receiveonly", f.Type)
	conns, _ := c.Connections(ctx)
	check(src.Name+" is online", conns.Connections[src.Device].Connected, "")
	check(src.Name+" isn't changing its mods", !inv.Busy, "")
	same, known := mods.SameVersion(inv.Version, mods.GameVersion(p.gameDir))
	vd := ""
	if !known {
		vd = "couldn't compare the versions"
	} else if !same {
		vd = "update the game on both PCs first"
	}
	check("Same game version as "+src.Name, same, vd)
	check("Every file name can be synced", len(inv.Skipped) == 0, strings.Join(inv.Skipped, ", "))
	var snapBytes int64
	for _, rel := range append(append([]string{}, p.diff.Changed...), p.diff.Removed...) {
		if fi, err := os.Stat(filepath.Join(f.Path, filepath.FromSlash(rel))); err == nil {
			snapBytes += fi.Size()
		}
	}
	free, ferr := diskFree(f.Path)
	need := uint64(float64(p.diff.Bytes)*1.1) + 256<<20
	check("Enough free space for the update", ferr == nil && free >= need, fmt.Sprintf("needs %s", humanBytes(int64(need))))
	sfree, serr := diskFree(paths.Root(paths.Local))
	sneed := uint64(snapBytes) + 256<<20
	check("Enough free space to save what it replaces", serr == nil && sfree >= sneed, fmt.Sprintf("needs %s", humanBytes(int64(sneed))))
	if pv.NeedConfirm {
		check("Removals confirmed", confirmDeletes, fmt.Sprintf("removes %d files, %d plugins", len(p.diff.Removed), len(p.diff.RemovedPlugins)))
	}
	pv.Ready = mods.Passed(pv.Checks)
	p.preview = pv
	return p, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// ModUpdatePreview says what applying the waiting update would do.
func (a *App) ModUpdatePreview(id string) (ModPreview, error) {
	c, err := a.client()
	if err != nil {
		return ModPreview{}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	p, err := planUpdate(ctx, c, id, false)
	if err != nil {
		return ModPreview{}, err
	}
	return p.preview, nil
}

// applyingSet tracks updates running in this process.
type applyingSet struct {
	sync.Mutex
	m map[string]bool
}

func (s *applyingSet) has(id string) bool {
	s.Lock()
	defer s.Unlock()
	return s.m[id]
}

func (s *applyingSet) start(id string) bool {
	s.Lock()
	defer s.Unlock()
	if s.m == nil {
		s.m = map[string]bool{}
	}
	if s.m[id] {
		return false
	}
	s.m[id] = true
	return true
}

func (s *applyingSet) done(id string) {
	s.Lock()
	delete(s.m, id)
	s.Unlock()
}

var applying applyingSet

// ApplyModUpdate applies the update the source PC published: after every
// gate passes, it saves the files the update replaces, lets exactly the
// mod files sync, and checks the result. It returns once the update has
// started; progress shows on the folder.
func (a *App) ApplyModUpdate(id string, confirmDeletes bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if !applying.start(id) {
		return errors.New("already applying an update")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	p, err := planUpdate(ctx, c, id, confirmDeletes)
	cancel()
	if err != nil {
		applying.done(id)
		return err
	}
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "gate", Gen: p.inv.Gen, Checks: p.preview.Checks,
		Summary: fmt.Sprintf("Update to version %d from %s: +%d ~%d -%d files", p.inv.Gen, p.src.Name, p.preview.Added, p.preview.Changed, p.preview.Removed)})
	if !p.preview.Ready {
		applying.done(id)
		return errors.New("can't apply yet: " + strings.Join(mods.Failed(p.preview.Checks), "; "))
	}

	// Before anything changes: what the game's own files are, and a copy of
	// every mod file the update replaces or removes.
	outside, err := mods.Outside(p.path, mods.Scope(p.prev, &p.inv))
	if err != nil {
		applying.done(id)
		return fmt.Errorf("couldn't list the game's files: %w", err)
	}
	sn, err := mods.TakeSnapshot(id, p.game, p.path, p.prev, append(append([]string{}, p.diff.Changed...), p.diff.Removed...), p.diff.Added)
	if err != nil {
		applying.done(id)
		mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "before", Gen: p.inv.Gen, Summary: "Update not started",
			Checks: []mods.Check{{Name: "Files it replaces saved", OK: false, Detail: err.Error()}}})
		return err
	}
	if err := saveJSON(invPath(id, "target"), &p.inv); err != nil {
		applying.done(id)
		return err
	}
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		m.Phase, m.Held, m.Started = phaseApplying, "", time.Now()
		st.ModSync[id] = m
	})
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "before", Gen: p.inv.Gen,
		Summary: fmt.Sprintf("Saved %d files (%s) before the update", len(sn.Copied), humanBytes(sn.Bytes)),
		Checks: []mods.Check{
			{Name: "Files it replaces saved", OK: true, Detail: fmt.Sprintf("%d files, snapshot %s", len(sn.Copied), sn.Stamp)},
			{Name: "Game files recorded", OK: true, Detail: fmt.Sprintf("%d files", len(outside))},
		}})
	runtime.EventsEmit(a.ctx, "changed")
	go a.runApply(p, outside)
	return nil
}

// runApply lets the update sync, then checks it.
func (a *App) runApply(p *applyPlan, outside map[string]mods.Stamp) {
	defer applying.done(p.id)
	defer runtime.EventsEmit(a.ctx, "changed")
	limit := 10*time.Minute + time.Duration(p.diff.Bytes/(5<<20))*time.Second // at least 5 MB/s
	if limit > 2*time.Hour {
		limit = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(a.ctx, limit)
	defer cancel()
	c, err := a.client()
	var syncErr error
	if err == nil {
		syncErr = letSync(ctx, c, p)
		pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = c.PatchFolder(pctx, p.id, map[string]any{"paused": true})
		pcancel()
	} else {
		syncErr = err
	}

	checks := []mods.Check{{Name: "Update synced", OK: syncErr == nil, Detail: errText(syncErr)}}
	// The plugin lists (load order) go with the files.
	for name, content := range p.inv.PluginLists {
		checks = append(checks, writePluginList(p.game, name, content))
	}
	checks = append(checks, mods.CheckApplied(p.path, p.inv, p.diff.Removed)...)
	after, err := mods.Outside(p.path, mods.Scope(p.prev, &p.inv))
	if err != nil {
		checks = append(checks, mods.Check{Name: "Game files untouched", OK: false, Detail: err.Error()})
	} else {
		checks = append(checks, mods.CheckOutside(outside, after))
	}
	if pc, ok := mods.CheckPlugins(p.game, p.path); ok {
		checks = append(checks, pc)
	}
	ok := mods.Passed(checks)
	entry := mods.AuditEntry{Folder: p.id, Label: p.label, Phase: "after", Gen: p.inv.Gen, Checks: checks}
	if ok {
		entry.Summary = fmt.Sprintf("Updated to version %d from %s", p.inv.Gen, p.src.Name)
		_ = saveJSON(invPath(p.id, "applied"), &p.inv)
		_ = saveJSON(invPath(p.id, "target"), nil)
		store.UpdateState(func(st *store.State) {
			m := st.ModSync[p.id]
			m.Phase, m.Held, m.AppliedGen, m.LastAudit = phaseIdle, "", p.inv.Gen, time.Now()
			st.ModSync[p.id] = m
		})
		if c != nil {
			sctx, scancel := context.WithTimeout(a.ctx, 30*time.Second)
			_ = applyExclusions(sctx, c, p.id, folderIgnores(store.LoadSettings(), p.id, p.path))
			scancel()
		}
		mods.PruneSnapshots(p.id, 3, 14*24*time.Hour)
		runtime.EventsEmit(a.ctx, "toast", fmt.Sprintf("Mods for %s updated from %s", p.label, p.src.Name))
		logx.Printf("deployed mods %s: applied version %d from %s", p.label, p.inv.Gen, p.src.Name)
	} else {
		why := strings.Join(mods.Failed(checks), "; ")
		entry.Summary = "Update failed its checks: " + why
		holdMods(context.Background(), nil, p.id, "the last update failed its checks: roll it back or apply it again")
		store.UpdateState(func(st *store.State) {
			m := st.ModSync[p.id]
			m.LastAudit = time.Now()
			st.ModSync[p.id] = m
		})
		runtime.EventsEmit(a.ctx, "toast", fmt.Sprintf("The mod update for %s failed its checks; see its audit to roll it back", p.label))
		logx.Printf("deployed mods %s: update to version %d failed its checks: %s", p.label, p.inv.Gen, why)
	}
	mods.LogAudit(entry)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// letSync opens the scope to the old and new mod files, resumes the folder,
// puts back anything changed here, and waits until it holds the update.
func letSync(ctx context.Context, c *syncthing.Client, p *applyPlan) error {
	if err := applyExclusions(ctx, c, p.id, folderIgnores(store.LoadSettings(), p.id, p.path)); err != nil {
		return fmt.Errorf("couldn't set which files sync: %w", err)
	}
	if err := c.PatchFolder(ctx, p.id, map[string]any{"paused": false}); err != nil {
		return err
	}
	_ = c.Rescan(ctx, p.id)
	reverted := false
	for {
		st, err := c.FolderStatus(ctx, p.id)
		if err == nil && st.State == "idle" {
			if st.ReceiveOnlyTotalItems > 0 && !reverted {
				// Mod files changed or added here: the snapshot has them;
				// the source's version wins.
				if err := c.Revert(ctx, p.id); err != nil {
					return fmt.Errorf("couldn't put back files changed here: %w", err)
				}
				reverted = true
			} else if st.NeedFiles == 0 && st.NeedBytes == 0 && arrived(p) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("the update didn't finish in time (is the source PC still on?)")
		case <-time.After(3 * time.Second):
		}
	}
}

// arrived is a quick check (sizes only) that every file of the update is here.
func arrived(p *applyPlan) bool {
	for _, f := range p.inv.Files {
		fi, err := os.Lstat(filepath.Join(p.path, filepath.FromSlash(f.Rel)))
		if err != nil || fi.Size() != f.Size {
			return false
		}
	}
	for _, rel := range p.diff.Removed {
		if _, err := os.Lstat(filepath.Join(p.path, filepath.FromSlash(rel))); err == nil {
			return false
		}
	}
	return true
}

// writePluginList writes one of the source's plugin lists here.
func writePluginList(game, name, content string) mods.Check {
	c := mods.Check{Name: "Load order (" + name + ") updated", OK: true}
	p := mods.PluginListPath(game, name)
	if p == "" {
		c.Detail = "this game has none"
		return c
	}
	if err := mods.ValidPluginList(content); err != nil {
		c.OK, c.Detail = false, err.Error()
		return c
	}
	if b, err := os.ReadFile(p); err == nil && string(b) == content {
		c.Detail = "unchanged"
		return c
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		c.OK, c.Detail = false, err.Error()
		return c
	}
	if err := os.WriteFile(p+".syncer-tmp", []byte(content), 0o644); err != nil {
		c.OK, c.Detail = false, err.Error()
		return c
	}
	if err := os.Rename(p+".syncer-tmp", p); err != nil {
		_ = os.Remove(p + ".syncer-tmp")
		c.OK, c.Detail = false, err.Error()
	}
	return c
}

// ---- rollback and audits -------------------------------------------------------

// SnapshotView is a snapshot, for the UI.
type SnapshotView struct {
	Stamp   string    `json:"stamp"`
	Created time.Time `json:"created"`
	Gen     int64     `json:"gen"`
	Files   int       `json:"files"`
	Added   int       `json:"added"`
	Bytes   int64     `json:"bytes"`
}

// ModSnapshots lists the snapshots taken before updates of a folder.
func (a *App) ModSnapshots(id string) []SnapshotView {
	var out []SnapshotView
	for _, sn := range mods.Snapshots(id) {
		out = append(out, SnapshotView{Stamp: sn.Stamp, Created: sn.Created, Gen: sn.Gen, Files: len(sn.Copied), Added: len(sn.Added), Bytes: sn.Bytes})
	}
	return out
}

// RollbackMods puts a deployed-mods folder back the way it was before an
// update: the files it replaced return and the files it added go.
func (a *App) RollbackMods(id, stamp string) error {
	s := store.LoadSettings()
	mf, ok := s.Mods[id]
	if !ok || mf.Kind != mods.KindDeployed || mf.Role == meta.RoleSource {
		return errors.New("not a folder receiving deployed mods")
	}
	if applying.has(id) {
		return errors.New("an update is being applied")
	}
	procs := processPaths()
	gameDir := mods.GameDir(mf.Game)
	if mods.ManagerRunning(procs) || gameRunning(gameDir, procs) {
		return errors.New("close Vortex and the game first")
	}
	var sn *mods.Snapshot
	for _, x := range mods.Snapshots(id) {
		if x.Stamp == stamp {
			x := x
			sn = &x
		}
	}
	if sn == nil {
		return errors.New("that snapshot is gone")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return err
	}
	if err := c.PatchFolder(ctx, id, map[string]any{"paused": true}); err != nil {
		return err
	}
	prev := sn.Inventory()
	target := loadInv(id, "target")
	outside, err := mods.Outside(f.Path, mods.Scope(prev, target, loadInv(id, "applied")))
	if err != nil {
		return err
	}
	rerr := mods.Rollback(*sn, f.Path)
	checks := []mods.Check{{Name: "Files put back", OK: rerr == nil, Detail: errText(rerr)}}
	if prev != nil {
		checks = append(checks, mods.CheckApplied(f.Path, *prev, nil)...)
	}
	after, err := mods.Outside(f.Path, mods.Scope(prev, target, loadInv(id, "applied")))
	if err == nil {
		checks = append(checks, mods.CheckOutside(outside, after))
	}
	ok = mods.Passed(checks)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "rollback", Gen: sn.Gen, Checks: checks,
		Summary: fmt.Sprintf("Rolled back to before the update of %s", sn.Created.Local().Format("2 Jan 15:04"))})
	if !ok {
		holdMods(ctx, c, id, "the rollback failed its checks: "+strings.Join(mods.Failed(checks), "; "))
		runtime.EventsEmit(a.ctx, "changed")
		return errors.New("rolled back, but some checks failed: see the audit")
	}
	_ = saveJSON(invPath(id, "applied"), prev)
	_ = saveJSON(invPath(id, "target"), nil)
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		m.Phase, m.Held, m.AppliedGen, m.LastAudit = phaseIdle, "", sn.Gen, time.Now()
		st.ModSync[id] = m
	})
	_ = applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, f.Path))
	logx.Printf("deployed mods %s: rolled back to %s", cmpOr(f.Label, id), sn.Stamp)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// ModAudit lists a folder's audit log, newest first.
func (a *App) ModAudit(id string) []mods.AuditEntry { return mods.AuditLog(id) }

// RunModAudit checks a deployed-mods folder now: that it holds exactly the
// files last applied (or sent). It changes nothing.
func (a *App) RunModAudit(id string) (mods.AuditEntry, error) {
	s := store.LoadSettings()
	mf, ok := s.Mods[id]
	if !ok || mf.Kind != mods.KindDeployed {
		return mods.AuditEntry{}, errors.New("not a deployed-mods folder")
	}
	c, err := a.client()
	if err != nil {
		return mods.AuditEntry{}, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return mods.AuditEntry{}, err
	}
	var inv *mods.Inventory
	if mf.Role == meta.RoleSource {
		if ss := loadSource(id); ss != nil {
			inv = &ss.Inv
		}
	} else {
		inv = loadInv(id, "applied")
	}
	e := mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "check"}
	if inv == nil {
		e.Summary = "Nothing applied yet"
		e.Checks = []mods.Check{{Name: "Update applied", OK: true, Warn: true, Detail: "none yet"}}
	} else {
		e.Gen = inv.Gen
		e.Checks = mods.CheckApplied(f.Path, *inv, nil)
		if pc, ok := mods.CheckPlugins(mf.Game, f.Path); ok {
			e.Checks = append(e.Checks, pc)
		}
		wantType := "receiveonly"
		if mf.Role == meta.RoleSource {
			wantType = "sendonly"
		}
		e.Checks = append(e.Checks, mods.Check{Name: "Folder type", OK: f.Type == wantType, Detail: f.Type})
		e.Summary = fmt.Sprintf("Checked version %d", inv.Gen)
	}
	mods.LogAudit(e)
	e.OK = mods.Passed(e.Checks)
	store.UpdateState(func(st *store.State) {
		m := st.ModSync[id]
		m.LastAudit = time.Now()
		if st.ModSync == nil {
			st.ModSync = map[string]store.ModSyncState{}
		}
		st.ModSync[id] = m
	})
	return e, nil
}

// ReleaseModHold lets a held folder take updates again (after the user
// looked at why it was held). The folder stays paused until an update.
func (a *App) ReleaseModHold(id string) error {
	if applying.has(id) {
		return errors.New("an update is being applied")
	}
	if store.LoadSettings().Mods[id].Role == meta.RoleSource {
		c, err := a.client()
		if err != nil {
			return err
		}
		ctx, cancel := a.callCtx()
		defer cancel()
		resumeSource(ctx, c, id) // held again at once if it still can't be read
	} else {
		store.UpdateState(func(st *store.State) {
			m, ok := st.ModSync[id]
			if !ok {
				return
			}
			m.Phase, m.Held = phasePending, ""
			st.ModSync[id] = m
		})
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// ---- view ------------------------------------------------------------------------

// modView fills in a deployed-mods folder's update state.
func (a *App) modView(v *FolderView) {
	if v.Kind != mods.KindDeployed {
		return
	}
	ms := store.LoadState().ModSync[v.ID]
	v.ModPhase, v.ModHeld = cmpOr(ms.Phase, phaseIdle), ms.Held
	src, ok := meta.ModSource(v.ID)
	if v.ModRole == meta.RoleSource {
		if ss := loadSource(v.ID); ss != nil {
			if ss.Inv.Busy && ms.Phase != phaseHeld {
				v.ModPhase = phaseBusy
			}
			v.ModPending = fmt.Sprintf("version %d · %d files", ss.Inv.Gen, len(ss.Inv.Files))
		}
		return
	}
	if !ok {
		v.ModPending = "no PC sends these mods right now"
		return
	}
	if inv, ok := meta.ModInventories(src.Device)[v.ID]; ok && inv.Gen > ms.AppliedGen {
		v.ModPending = fmt.Sprintf("version %d from %s", inv.Gen, src.Name)
		if v.ModPhase == phaseIdle {
			v.ModPhase = phasePending
		}
	} else {
		v.ModPending = "from " + src.Name
	}
}

// deployedSize measures only the files the deployment has.
func deployedSize(f mods.Found) (int64, int, time.Time) {
	var size int64
	var files int
	var mod time.Time
	for _, df := range f.Deploy.Files {
		if fi, err := os.Lstat(filepath.Join(f.Path, filepath.FromSlash(df.RelPath))); err == nil && fi.Mode().IsRegular() {
			size += fi.Size()
			files++
			if fi.ModTime().After(mod) {
				mod = fi.ModTime()
			}
		}
	}
	return size, files, mod
}
