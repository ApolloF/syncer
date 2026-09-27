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
//     ("receive only"), from that one PC only: a receiver's folder is shared
//     with its source and no one else. A PC that claims the source role later
//     is only followed once the user agrees, on the old source and on every
//     receiver, and only if Syncthing confirms that PC wrote the claim.
//   - Only the files in Vortex's deployment are in scope: the .stignore
//     un-ignores exactly those files and ignores everything else, so the
//     game's own files are never scanned or sent.
//   - The source publishes an inventory of its deployment (sizes, hashes,
//     game version, plugin lists), so a receiver knows exactly what an
//     update changes before anything arrives.
//   - A receiver records which files were in the folder before Syncer first
//     changed it (the game's own). An update that would replace one needs
//     the user's say-so, the game's copy is kept for good, and it comes back
//     when the mod goes away; the game's own files are never deleted.
//   - A receiver's folder stays paused. It only runs during an update the
//     user applies, after gates (same game version, Vortex and the game
//     closed, source verified, online and not mid-change, enough disk space,
//     removals, game files and program files confirmed), a snapshot of every
//     file the update replaces, and an audit of the game's own files. After
//     it everything is checked again; if anything is off the folder is held
//     and can be rolled back.

// ---- local state ------------------------------------------------------------------

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
	// Tombstones are files removed from the deployment (and gone from the
	// disk), kept in scope for a while so their deletion reaches receivers.
	Tombstones map[string]time.Time `json:"tombstones,omitempty"`
	Hashes     mods.HashCache       `json:"hashes,omitempty"`
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

// saveInv writes (inv != nil) or removes an inventory file.
func saveInv(id, kind string, inv *mods.Inventory) error {
	if inv == nil {
		return saveJSON(invPath(id, kind), nil)
	}
	return saveJSON(invPath(id, kind), inv)
}

// baseline is the set of files (lower-cased) in a receiver's folder before
// Syncer first changed it: the game's own.
func loadBaseline(id string) map[string]bool {
	var m map[string]bool
	if !paths.ValidID(id) || !loadJSON(invPath(id, "baseline"), &m) {
		return nil
	}
	return m
}

// recordBaseline notes the files in target outside scope as the game's own.
func recordBaseline(id, target string, scope map[string]bool) (map[string]bool, error) {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		m := map[string]bool{} // nothing there yet
		return m, saveJSON(invPath(id, "baseline"), m)
	}
	out, err := mods.Outside(target, scope)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(out))
	for k := range out {
		m[k] = true
	}
	return m, saveJSON(invPath(id, "baseline"), m)
}

func forgetModFiles(id string) {
	if !paths.ValidID(id) {
		return
	}
	for _, k := range []string{"source", "applied", "target", "baseline"} {
		_ = os.Remove(invPath(id, k))
	}
}

// updateModSync changes one folder's mod state.
func updateModSync(id string, fn func(m *store.ModSyncState)) {
	store.UpdateState(func(st *store.State) {
		if st.ModSync == nil {
			st.ModSync = map[string]store.ModSyncState{}
		}
		m := st.ModSync[id]
		fn(&m)
		st.ModSync[id] = m
	})
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

// Why a deployed-mods folder is held (store.ModSyncState.HeldBy).
const (
	heldOff         = "off"         // the experimental option is off
	heldUnreadable  = "unreadable"  // the source can't read Vortex's deployment
	heldHandover    = "handover"    // another PC claims to send these mods
	heldCheck       = "check"       // an update or rollback failed its checks
	heldInterrupted = "interrupted" // an update was cut off
)

// ---- source -----------------------------------------------------------------------

// addDeployedSource starts sending this PC's deployed mods of a game.
func (a *App) addDeployedSource(f mods.Found) error {
	s := store.LoadSettings()
	if !s.SyncDeployedMods {
		return errors.New(`turn on "Sync deployed mods in the game folder" in Settings first`)
	}
	if err := mods.CheckModSyncable(mods.KindDeployed, f.Path); err != nil {
		return err
	}
	if len(f.Warn) > 0 && f.Deploy != nil {
		if w := deployWarning(f); w != "" {
			return errors.New(w)
		}
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
	if err := a.becomeSource(ctx, c, st.MyID, id, f.Label(), f.Game, f.Root, f.Rel, f.Path, f.GameDir, false); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// deployWarning is the reason a found deployment can't be sent ("" = it can).
func deployWarning(f mods.Found) string {
	for _, w := range f.Warn {
		if strings.Contains(w, "symlink") || strings.Contains(w, "unknown Vortex deployment") {
			return w
		}
	}
	return ""
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
	if applying.has(id) {
		return errors.New("an update is being applied")
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
	if err := a.becomeSource(ctx, c, st.MyID, id, cmpOr(f.Label, id), mf.Game, mf.Root, mf.Rel, f.Path, mods.GameDir(mf.Game), true); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// becomeSource builds the inventory of the deployment at path and starts
// sending it (a new folder, or an existing one this PC received).
func (a *App) becomeSource(ctx context.Context, c *syncthing.Client, me, id, label, game, root, rel, path, gameDir string, existing bool) error {
	procs := processPaths()
	if mods.ManagerRunning(procs) {
		return errors.New("close Vortex first")
	}
	if gameRunning(gameDir, procs) {
		return errors.New("close the game first")
	}
	hashes := mods.HashCache{}
	inv, err := mods.BuildInventory(game, path, gameDir, hashes)
	if err != nil {
		return err
	}
	if len(inv.Files) == 0 {
		return errors.New("Vortex hasn't deployed any mods for this game here")
	}
	inv.Folder, inv.Gen = id, newGen(0)
	ss := &sourceState{Inv: inv, Stamp: sourceStamp(game, path), Built: time.Now(), Hashes: hashes}
	if err := saveJSON(invPath(id, "source"), ss); err != nil {
		return err
	}
	sf := meta.SharedFolder{ID: id, Label: label, Root: root, Rel: rel, Kind: mods.KindDeployed, ModGame: game}
	if err := meta.RegisterMod(id, mods.NameOf(game), sf); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[id]
		mf.Role = meta.RoleSource
		s.Mods[id] = mf
		delete(s.Ignored, id)
		delete(s.Dismissed, dismissKey(path))
	})
	updateModSync(id, func(m *store.ModSyncState) {
		*m = store.ModSyncState{Phase: phaseIdle, SourceGen: inv.Gen, SourceHash: inv.Hash, Since: time.Now().UnixNano()}
	})
	publishInventories(me)
	if existing {
		if err := applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, path)); err != nil {
			return err
		}
		patch := map[string]any{"type": "sendonly", "paused": false, "devices": devicesFor(me, peerIDs(ctx, c, me))}
		if err := c.PatchFolder(ctx, id, patch); err != nil {
			return err
		}
		_ = saveInv(id, "applied", nil)
		_ = saveInv(id, "target", nil)
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
		Summary: fmt.Sprintf("This PC sends %d deployed mod files", len(inv.Files)),
		Checks: []mods.Check{{Name: "Deployment read", OK: true, Detail: fmt.Sprintf("%d files, %d skipped", len(inv.Files), len(inv.Skipped))},
			{Name: "Game version recorded", OK: inv.Version != (mods.Version{}), Warn: inv.Version == (mods.Version{})}}})
	logx.Printf("sending deployed mods %s (%s) as %s", label, path, id)
	return nil
}

// newGen is a new inventory generation: after prev, and unique across this
// PC's two processes (the window and the background task).
func newGen(prev int64) int64 { return max(prev+1, time.Now().UnixNano()) }

// devicesFor is a folder's device list.
func devicesFor(me string, others []string) []map[string]string {
	l := []map[string]string{{"deviceID": me}}
	for _, o := range others {
		if o != me {
			l = append(l, map[string]string{"deviceID": o})
		}
	}
	return l
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

// ---- keeping deployed folders in line (every tick) ------------------------------

// deployedTick runs from modsTick. The source rebuilds its inventory when
// Vortex deployed something new; a receiver keeps its folder paused and
// notes when an update waits. A PC claiming the source role later is only
// followed once the user agrees.
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
		ms := store.LoadState().ModSync[id]
		if !s.SyncDeployedMods {
			if ms.Phase != phaseApplying && (ms.Phase != phaseHeld || ms.HeldBy != heldOff || !f.Paused) {
				holdMods(ctx, c, id, heldOff, "the experimental option to sync deployed mods is off")
				changed = true
			}
			continue
		}
		if ms.HeldBy == heldOff { // turned back on
			updateModSync(id, func(m *store.ModSyncState) { m.Phase, m.Held, m.HeldBy = phasePending, "", "" })
			ms = store.LoadState().ModSync[id]
			if mf.Role == meta.RoleSource {
				resumeSource(ctx, c, id)
			}
			changed = true
		}
		if mf.Role == meta.RoleSource {
			changed = sourceTick(ctx, c, me, id, mf, f, ms, procs) || changed
		} else {
			changed = receiverTick(ctx, c, me, id, f, ms) || changed
		}
	}
	if me != "" {
		publishInventories(me)
	}
	return changed
}

// newerClaim is a verified claim to send folder id made by another PC after
// since.
func newerClaim(ctx context.Context, c *syncthing.Client, me, id string, since int64) (meta.Source, bool) {
	src, ok := meta.ModSource(id)
	if !ok || src.Device == me || src.Since <= since {
		return meta.Source{}, false
	}
	if err := meta.WrittenBy(ctx, c, src.Device); err != nil {
		warnOnceMods(id+src.Device, "mods: ignoring %s's claim to send %s: %v", src.Name, id, err)
		return meta.Source{}, false
	}
	return src, true
}

func sourceTick(ctx context.Context, c *syncthing.Client, me, id string, mf store.ModFolder, f syncthing.Folder, ms store.ModSyncState, procs []string) bool {
	if src, ok := newerClaim(ctx, c, me, id, ms.Since); ok && ms.Handover != src.Device {
		updateModSync(id, func(m *store.ModSyncState) { m.Handover = src.Device })
		holdMods(ctx, c, id, heldHandover, src.Name+" says it sends these mods now: open the audit to let it, or keep sending from this PC")
		return true
	}
	if ms.Phase == phaseHeld && ms.HeldBy != heldUnreadable {
		return false // waiting for the user
	}
	ss := loadSource(id)
	if ss == nil {
		holdMods(ctx, c, id, heldCheck, "this PC's list of deployed files is missing: remove the folder and send it again")
		return true
	}
	gameDir := mods.GameDir(mf.Game)
	busy := mods.ManagerRunning(procs) || gameRunning(gameDir, procs)
	if busy != ss.Inv.Busy {
		ss.Inv.Busy = busy
		if !busy {
			ss.Stamp = "" // Vortex may have deployed: look again
		}
		_ = saveJSON(invPath(id, "source"), ss)
	}
	if busy {
		return false
	}
	stamp := sourceStamp(mf.Game, f.Path)
	if stamp == ss.Stamp && time.Since(ss.Built) < 6*time.Hour {
		return false
	}
	if ss.Hashes == nil {
		ss.Hashes = mods.HashCache{}
	}
	inv, err := mods.BuildInventory(mf.Game, f.Path, gameDir, ss.Hashes)
	if err != nil {
		ss.Inv.Busy = true // receivers wait until it's readable again
		_ = saveJSON(invPath(id, "source"), ss)
		holdMods(ctx, c, id, heldUnreadable, "can't read Vortex's deployment: "+err.Error())
		return true
	}
	ss.Stamp, ss.Built = stamp, time.Now()
	wasHeld := ms.Phase == phaseHeld
	if wasHeld { // readable again: send again
		resumeSource(ctx, c, id)
	}
	if inv.Hash == ss.Inv.Hash {
		_ = saveJSON(invPath(id, "source"), ss)
		return wasHeld
	}
	if ss.Tombstones == nil {
		ss.Tombstones = map[string]time.Time{}
	}
	now := mods.Scope(&inv)
	for _, old := range ss.Inv.Files {
		// Only files really gone: one still here (Vortex put the game's own
		// file back) must not be sent as if it were a mod.
		if _, err := os.Lstat(filepath.Join(f.Path, filepath.FromSlash(old.Rel))); !now[strings.ToLower(old.Rel)] && err != nil {
			ss.Tombstones[old.Rel] = time.Now()
		}
	}
	for rel, t := range ss.Tombstones {
		_, err := os.Lstat(filepath.Join(f.Path, filepath.FromSlash(rel)))
		if now[strings.ToLower(rel)] || err == nil || time.Since(t) > tombstoneAge {
			delete(ss.Tombstones, rel)
		}
	}
	for k := range ss.Hashes {
		if !now[k] {
			delete(ss.Hashes, k)
		}
	}
	inv.Folder, inv.Gen = id, newGen(ss.Inv.Gen)
	ss.Inv = inv
	if err := saveJSON(invPath(id, "source"), ss); err != nil {
		return false
	}
	updateModSync(id, func(m *store.ModSyncState) { m.SourceGen, m.SourceHash = inv.Gen, inv.Hash })
	if err := applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, f.Path)); err != nil {
		logx.Printf("mods: scope of %s: %v", id, err)
	}
	_ = c.Rescan(ctx, id)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "source", Gen: inv.Gen,
		Summary: fmt.Sprintf("Vortex's deployment changed: %d files", len(inv.Files)),
		Checks:  []mods.Check{{Name: "Deployment read", OK: true, Detail: fmt.Sprintf("%d files, %d skipped", len(inv.Files), len(inv.Skipped))}}})
	logx.Printf("deployed mods %s: new version (%d files)", cmpOr(f.Label, id), len(inv.Files))
	return true
}

// resumeSource lets a source folder that was held send again.
func resumeSource(ctx context.Context, c *syncthing.Client, id string) {
	updateModSync(id, func(m *store.ModSyncState) { m.Phase, m.Held, m.HeldBy = phaseIdle, "", "" })
	if c != nil && !store.LoadSettings().Paused() && !slices.Contains(store.LoadState().ModHeld, id) {
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

// HandOverMods lets the PC that claimed to send a deployed-mods folder
// later do so: this PC receives from it from now on.
func (a *App) HandOverMods(id string) error {
	ms := store.LoadState().ModSync[id]
	mf := store.LoadSettings().Mods[id]
	if mf.Role != meta.RoleSource || ms.Handover == "" {
		return errors.New("no other PC asked to send these mods")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	src, ok := meta.ModSource(id)
	if !ok || src.Device != ms.Handover {
		return errors.New("that PC no longer claims to send these mods")
	}
	if err := meta.WrittenBy(ctx, c, src.Device); err != nil {
		return err
	}
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return err
	}
	if err := demoteSource(ctx, c, id, f, src); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// demoteSource makes this PC a receiver of a folder src sends now. What it
// deployed itself counts as applied, and everything else in the folder as
// the game's own.
func demoteSource(ctx context.Context, c *syncthing.Client, id string, f syncthing.Folder, src meta.Source) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	var own *mods.Inventory
	if ss := loadSource(id); ss != nil {
		inv := ss.Inv
		inv.Busy = false
		own = &inv
	}
	if _, err := recordBaseline(id, f.Path, mods.Scope(own)); err != nil {
		return fmt.Errorf("couldn't list the game's files: %w", err)
	}
	if err := c.PatchFolder(ctx, id, map[string]any{"type": "receiveonly", "paused": true, "maxConflicts": 0,
		"devices": devicesFor(st.MyID, []string{src.Device})}); err != nil {
		return err
	}
	_ = saveInv(id, "applied", own)
	_ = saveJSON(invPath(id, "source"), nil)
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[id]
		mf.Role = meta.RoleReceiver
		s.Mods[id] = mf
	})
	updateModSync(id, func(m *store.ModSyncState) {
		*m = store.ModSyncState{Phase: phasePending, AppliedGen: m.SourceGen, Source: src.Device}
	})
	_ = applyExclusions(ctx, c, id, folderIgnores(store.LoadSettings(), id, f.Path))
	publishInventories(st.MyID)
	_, _ = meta.Reconcile(ctx, c)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "source",
		Summary: fmt.Sprintf("%s sends these mods now; this PC receives them", src.Name),
		Checks:  []mods.Check{{Name: "Handed over", OK: true, Detail: src.Name}}})
	logx.Printf("deployed mods %s: %s is the source now, this PC receives", cmpOr(f.Label, id), src.Name)
	return nil
}

func receiverTick(ctx context.Context, c *syncthing.Client, me, id string, f syncthing.Folder, ms store.ModSyncState) bool {
	if ms.Phase == phaseApplying {
		if applying.has(id) {
			return false
		}
		// Another process (the window) may be applying it right now.
		if ms.PID != os.Getpid() && winx.ProcessAlive(ms.PID) && time.Since(ms.Started) < 3*time.Hour {
			return false
		}
		holdMods(ctx, c, id, heldInterrupted, "an update was interrupted: roll it back, or apply it again")
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
	if src, ok := newerClaim(ctx, c, me, id, 0); ok && ms.Source != "" && src.Device != ms.Source {
		updateModSync(id, func(m *store.ModSyncState) { m.Handover = src.Device })
		holdMods(ctx, c, id, heldHandover, src.Name+" says it sends these mods now: open the audit to take updates from it")
		return true
	}
	next := phaseIdle
	if ms.Source != "" {
		if inv, ok := meta.ModInventories(ms.Source)[id]; ok && inv.Gen > ms.AppliedGen {
			next = phasePending
		}
	}
	if next != ms.Phase {
		updateModSync(id, func(m *store.ModSyncState) { m.Phase = next })
		changed = true
	}
	return changed
}

// holdMods pauses a deployed-mods folder and holds its updates, saying why.
func holdMods(ctx context.Context, c *syncthing.Client, id, by, why string) {
	if c != nil {
		_ = c.PatchFolder(ctx, id, map[string]any{"paused": true})
	}
	if _, ok := store.LoadSettings().Mods[id]; !ok {
		return // removed meanwhile
	}
	updateModSync(id, func(m *store.ModSyncState) {
		if m.Phase == phaseHeld && m.Held == why {
			return
		}
		m.Phase, m.Held, m.HeldBy = phaseHeld, why, by
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
		if mf.Kind == mods.KindDeployed && !applying.has(id) {
			holdMods(ctx, c, id, heldOff, why)
		}
	}
	runtime.EventsEmit(a.ctx, "changed")
}

var (
	modWarnMu sync.Mutex
	modWarned = map[string]bool{}
)

func warnOnceMods(key, format string, args ...any) {
	modWarnMu.Lock()
	defer modWarnMu.Unlock()
	if !modWarned[key] {
		modWarned[key] = true
		logx.Printf(format, args...)
	}
}

// ---- receiving --------------------------------------------------------------------

// joinDeployed starts receiving deployed mods another PC sends. Nothing is
// changed yet: the folder is added paused, shared with that PC only, and
// the files in the game folder are noted as the game's own.
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
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	src, ok := meta.ModSource(v.ID)
	if !ok || src.Device == st.MyID {
		return errors.New("no other PC sends these mods right now")
	}
	if err := meta.WrittenBy(ctx, c, src.Device); err != nil {
		return fmt.Errorf("can't confirm %s sends these mods: %w", src.Name, err)
	}
	synced, err := syncthingFolders(ctx, c)
	if err != nil {
		return err
	}
	if err := overlapsSynced(v.Path, v.ID, synced); err != nil {
		return err
	}
	if _, err := recordBaseline(v.ID, v.Path, nil); err != nil {
		return fmt.Errorf("couldn't list the game's files: %w", err)
	}
	label := cmpOr(v.Label, v.ID)
	if err := meta.RegisterMod(v.ID, mods.NameOf(v.ModGame), v.SharedFolder); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		mf := s.Mods[v.ID]
		mf.Role = meta.RoleReceiver
		s.Mods[v.ID] = mf
		delete(s.Ignored, v.ID)
		delete(s.Dismissed, dismissKey(v.Path))
	})
	updateModSync(v.ID, func(m *store.ModSyncState) { *m = store.ModSyncState{Phase: phasePending, Source: src.Device} })
	if err := meta.AddFolderSpec(ctx, c, meta.ModSpec(v.ID, label, v.Path, mods.KindDeployed, false), st.MyID, []string{src.Device}); err != nil {
		_, _ = store.UpdateSettings(func(s *store.Settings) { forgetMod(s, v.ID) })
		store.UpdateState(func(st *store.State) { delete(st.ModSync, v.ID) })
		forgetModFiles(v.ID)
		return err
	}
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("receiving deployed mods %s (%s) from %s; nothing applied yet", label, v.Path, src.Name)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// ModPreview is what applying the waiting update would do, and whether it can.
type ModPreview struct {
	ID             string    `json:"id"`
	Label          string    `json:"label"`
	From           string    `json:"from"` // the source PC
	Updated        time.Time `json:"updated"`
	Added          int       `json:"added"`
	Changed        int       `json:"changed"`
	Removed        int       `json:"removed"`
	Same           int       `json:"same"`
	Bytes          int64     `json:"bytes"`
	RemovedPlugins []string  `json:"removedPlugins"`
	PluginLists    []string  `json:"pluginLists"` // plugin lists it writes
	// What needs the user's explicit OK: many or plugin removals, the game's
	// own files replaced, program files added or changed.
	NeedDeletes bool         `json:"needDeletes"`
	GameFiles   []string     `json:"gameFiles"`
	Code        []string     `json:"code"`
	Checks      []mods.Check `json:"checks"`
	Ready       bool         `json:"ready"`
}

// ApplyConfirm is what the user agreed to for an update.
type ApplyConfirm struct {
	Deletes   bool `json:"deletes"`
	GameFiles bool `json:"gameFiles"`
	Code      bool `json:"code"`
}

// applyPlan is a checked update, ready to apply.
type applyPlan struct {
	id, label, game, path, gameDir string
	folder                         syncthing.Folder
	src                            meta.Source
	inv                            mods.Inventory
	prev                           *mods.Inventory
	diff                           mods.Diff
	replaces                       []string // the game's own files it replaces (kept first)
	restore                        []string // the game's own files to put back (their mod goes)
	preview                        ModPreview
}

// diskFree is the free space for this user on path's drive (a variable for tests).
var diskFree = winx.DiskFree

// planUpdate checks whether the waiting update of id can be applied.
func planUpdate(ctx context.Context, c *syncthing.Client, id string, ok ApplyConfirm) (*applyPlan, error) {
	s := store.LoadSettings()
	mf, found := s.Mods[id]
	if !found || mf.Kind != mods.KindDeployed {
		return nil, errors.New("not a deployed-mods folder")
	}
	if mf.Role == meta.RoleSource {
		return nil, errors.New("this PC sends these mods")
	}
	ms := store.LoadState().ModSync[id]
	if ms.Phase == phaseHeld && ms.HeldBy == heldHandover {
		return nil, errors.New("another PC claims to send these mods: open the audit first")
	}
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return nil, err
	}
	src, found := meta.ModSource(id)
	if !found || src.Device == st.MyID {
		return nil, errors.New("no other PC sends these mods right now")
	}
	inv, found := meta.ModInventories(src.Device)[id]
	if !found {
		return nil, fmt.Errorf("%s's list of deployed files hasn't arrived yet", src.Name)
	}
	p := &applyPlan{id: id, label: cmpOr(f.Label, id), game: mf.Game, path: f.Path, folder: f, src: src, inv: inv,
		prev: loadInv(id, "applied"), gameDir: mods.GameDir(mf.Game)}

	// The game's own files: never deleted, replaced only with the user's OK
	// (and kept), put back when the mod that replaced them goes.
	baseline := loadBaseline(id)
	if baseline == nil {
		if baseline, err = recordBaseline(id, f.Path, mods.Scope(p.prev)); err != nil {
			return nil, fmt.Errorf("couldn't list the game's files: %w", err)
		}
	}
	originals := mods.Originals(id)
	inTarget := mods.Scope(&inv)
	if p.prev != nil {
		kept := *p.prev
		kept.Files = nil
		for _, pf := range p.prev.Files {
			k := strings.ToLower(pf.Rel)
			switch {
			case !baseline[k] || inTarget[k]:
				kept.Files = append(kept.Files, pf)
			case originals[k] != "":
				kept.Files = append(kept.Files, pf)
				p.restore = append(p.restore, pf.Rel)
			} // else: the game's own file that no copy was kept of: out of scope, never deleted
		}
		p.prev = &kept
	}
	for _, nf := range inv.Files {
		if k := strings.ToLower(nf.Rel); baseline[k] && originals[k] == "" {
			if _, err := os.Lstat(filepath.Join(f.Path, filepath.FromSlash(nf.Rel))); err == nil {
				p.replaces = append(p.replaces, nf.Rel)
			}
		}
	}
	p.diff = mods.DiffLocal(f.Path, p.prev, inv)

	pv := ModPreview{ID: id, Label: p.label, From: src.Name, Updated: inv.Updated,
		Added: len(p.diff.Added), Changed: len(p.diff.Changed), Removed: len(p.diff.Removed), Same: p.diff.Same,
		Bytes: p.diff.Bytes, RemovedPlugins: p.diff.RemovedPlugins, GameFiles: capList(p.replaces, 50)}
	for n := range inv.PluginLists {
		pv.PluginLists = append(pv.PluginLists, n)
	}
	sort.Strings(pv.PluginLists)
	for _, rel := range append(append([]string{}, p.diff.Added...), p.diff.Changed...) {
		if mods.IsCode(rel) {
			pv.Code = append(pv.Code, rel)
		}
	}
	pv.Code = capList(pv.Code, 50)
	prevFiles := 0
	if p.prev != nil {
		prevFiles = len(p.prev.Files)
	}
	pv.NeedDeletes = len(p.diff.RemovedPlugins) > 0 || prevFiles >= 8 && len(p.diff.Removed)*4 > prevFiles

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
	check("Shared with "+src.Name+" only", sharedOnlyWith(f, st.MyID, src.Device), "")
	check("Updates come from "+src.Name, ms.Source == src.Device, "another PC claims to send them")
	verr := meta.WrittenBy(ctx, c, src.Device)
	check(src.Name+" sent this list itself", verr == nil, errText(verr))
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
	check("Every file name can be synced", len(inv.Skipped) == 0, strings.Join(capList(inv.Skipped, 10), ", "))
	check("No linked folders in the way", len(p.diff.Unsafe) == 0, strings.Join(capList(p.diff.Unsafe, 10), ", "))
	var snapBytes int64
	for _, rel := range append(append(append([]string{}, p.diff.Changed...), p.diff.Removed...), p.replaces...) {
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
	if pv.NeedDeletes {
		check("Removals confirmed", ok.Deletes, fmt.Sprintf("removes %d files, %d plugins", len(p.diff.Removed), len(p.diff.RemovedPlugins)))
	}
	if len(p.replaces) > 0 {
		check("Replacing game files confirmed", ok.GameFiles, fmt.Sprintf("%d of the game's own files", len(p.replaces)))
	}
	if len(pv.Code) > 0 {
		check("Program files confirmed", ok.Code, fmt.Sprintf("%d program files", len(pv.Code)))
	}
	pv.Ready = mods.Passed(pv.Checks)
	p.preview = pv
	return p, nil
}

// sharedOnlyWith reports whether a folder is shared with src and no other PC.
func sharedOnlyWith(f syncthing.Folder, me, src string) bool {
	has := false
	for _, d := range f.Devices {
		switch d.DeviceID {
		case me:
		case src:
			has = true
		default:
			return false
		}
	}
	return has
}

func capList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(xs[:n:n], fmt.Sprintf("and %d more", len(xs)-n))
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
	p, err := planUpdate(ctx, c, id, ApplyConfirm{})
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

// errApplying refuses to change a folder while an update is applied to it.
var errApplying = errors.New("an update is being applied to it; wait until it's done")

// ApplyModUpdate applies the update the source PC published: after every
// gate passes, it keeps the files the update replaces, lets exactly the
// mod files sync, and checks the result. It returns once the update has
// started; progress shows on the folder.
func (a *App) ApplyModUpdate(id string, ok ApplyConfirm) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if !applying.start(id) {
		return errors.New("already applying an update")
	}
	started := false
	defer func() {
		if !started {
			applying.done(id)
		}
	}()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	p, err := planUpdate(ctx, c, id, ok)
	cancel()
	if err != nil {
		return err
	}
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "gate", Gen: p.inv.Gen, Checks: p.preview.Checks,
		Summary: fmt.Sprintf("Update from %s: +%d ~%d -%d files", p.src.Name, p.preview.Added, p.preview.Changed, p.preview.Removed)})
	if !p.preview.Ready {
		return errors.New("can't apply yet: " + strings.Join(mods.Failed(p.preview.Checks), "; "))
	}

	// Before anything changes: what the game's own files are, a lasting copy
	// of each one the update replaces, and a snapshot of every file it
	// replaces or removes.
	outside, err := mods.Outside(p.path, mods.Scope(p.prev, &p.inv))
	if err != nil {
		return fmt.Errorf("couldn't list the game's files: %w", err)
	}
	for _, rel := range p.replaces {
		if err := mods.KeepOriginal(id, p.path, rel); err != nil {
			return fmt.Errorf("couldn't keep the game's own %s: %w", rel, err)
		}
	}
	var same []string // files the update shouldn't change: linked into the snapshot, in case it does
	for _, f := range p.inv.Files {
		same = append(same, f.Rel)
	}
	sn, err := mods.TakeSnapshot(id, p.game, p.path, p.prev,
		append(append(append([]string{}, p.diff.Changed...), p.diff.Removed...), p.replaces...), p.diff.Added, same)
	if err != nil {
		mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "before", Gen: p.inv.Gen, Summary: "Update not started",
			Checks: []mods.Check{{Name: "Files it replaces saved", OK: false, Detail: err.Error()}}})
		return err
	}
	// The applied list without the game's own files nothing was kept of:
	// they stay out of scope, so nothing can delete them.
	if err := saveInv(id, "applied", p.prev); err != nil {
		return err
	}
	if err := saveInv(id, "target", &p.inv); err != nil {
		return err
	}
	updateModSync(id, func(m *store.ModSyncState) {
		m.Phase, m.Held, m.HeldBy, m.Started, m.PID = phaseApplying, "", "", time.Now(), os.Getpid()
	})
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: p.label, Phase: "before", Gen: p.inv.Gen,
		Summary: fmt.Sprintf("Saved %d files (%s) before the update", len(sn.Copied), humanBytes(sn.Bytes)),
		Checks: []mods.Check{
			{Name: "Files it replaces saved", OK: true, Detail: fmt.Sprintf("%d files, snapshot %s", len(sn.Copied), sn.Stamp)},
			{Name: "Game files recorded", OK: true, Detail: fmt.Sprintf("%d files", len(outside))},
			{Name: "Game's own files it replaces kept", OK: true, Detail: fmt.Sprintf("%d files", len(p.replaces))},
		}})
	runtime.EventsEmit(a.ctx, "changed")
	started = true
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
		syncErr = letSync(ctx, c, p, folderIgnores(store.LoadSettings(), p.id, p.path))
		pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = c.PatchFolder(pctx, p.id, map[string]any{"paused": true})
		pcancel()
	} else {
		syncErr = err
	}

	checks := []mods.Check{{Name: "Update synced", OK: syncErr == nil, Detail: errText(syncErr)}}
	if syncErr == nil {
		// The game's own files whose mod went away come back.
		for _, rel := range p.restore {
			err := mods.RestoreOriginal(p.id, p.path, rel)
			checks = append(checks, mods.Check{Name: "Game's own " + rel + " put back", OK: err == nil, Detail: errText(err)})
		}
		// The plugin lists (load order) go with the files.
		for name, content := range p.inv.PluginLists {
			checks = append(checks, writePluginList(p.game, name, content))
		}
	}
	restored := map[string]bool{}
	for _, r := range p.restore {
		restored[strings.ToLower(r)] = true
	}
	var gone []string
	for _, r := range p.diff.Removed {
		if !restored[strings.ToLower(r)] {
			gone = append(gone, r)
		}
	}
	checks = append(checks, mods.CheckApplied(p.path, p.inv, gone)...)
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
	if _, still := store.LoadSettings().Mods[p.id]; !still {
		return // removed meanwhile (refused while applying, but be safe)
	}
	if ok {
		entry.Summary = fmt.Sprintf("Updated from %s", p.src.Name)
		_ = saveInv(p.id, "applied", &p.inv)
		_ = saveInv(p.id, "target", nil)
		updateModSync(p.id, func(m *store.ModSyncState) {
			m.Phase, m.Held, m.HeldBy, m.AppliedGen, m.LastAudit, m.PID = phaseIdle, "", "", p.inv.Gen, time.Now(), 0
		})
		if c != nil {
			sctx, scancel := context.WithTimeout(a.ctx, 30*time.Second)
			_ = applyExclusions(sctx, c, p.id, folderIgnores(store.LoadSettings(), p.id, p.path))
			scancel()
		}
		mods.PruneSnapshots(p.id, 3, 14*24*time.Hour)
		runtime.EventsEmit(a.ctx, "toast", fmt.Sprintf("Mods for %s updated from %s", p.label, p.src.Name))
		logx.Printf("deployed mods %s: applied the update from %s", p.label, p.src.Name)
	} else {
		why := strings.Join(mods.Failed(checks), "; ")
		entry.Summary = "Update failed its checks: " + why
		holdMods(context.Background(), nil, p.id, heldCheck, "the last update failed its checks: roll it back or apply it again")
		updateModSync(p.id, func(m *store.ModSyncState) { m.LastAudit, m.PID = time.Now(), 0 })
		runtime.EventsEmit(a.ctx, "toast", fmt.Sprintf("The mod update for %s failed its checks; see its audit to roll it back", p.label))
		logx.Printf("deployed mods %s: the update failed its checks: %s", p.label, why)
	}
	mods.LogAudit(entry)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// letSync applies an update in two steps. First the folder runs with
// everything ignored: it learns the source's file list and changes nothing,
// and that list must match the inventory (every file there, not deleted,
// the same size; removed files gone). Only then are the old and new mod
// files let in; the mod files changed here are put back (only ones the
// snapshot has), and it waits until the folder holds the update.
func letSync(ctx context.Context, c *syncthing.Client, p *applyPlan, ignores []string) error {
	block := append(withSyncIgnores(mods.KindDeployed, nil), "*")
	if err := applyExclusions(ctx, c, p.id, block); err != nil {
		return fmt.Errorf("couldn't set which files sync: %w", err)
	}
	if err := c.PatchFolder(ctx, p.id, map[string]any{"paused": false}); err != nil {
		return err
	}
	if err := checkIndex(ctx, c, p); err != nil {
		return err
	}
	if err := applyExclusions(ctx, c, p.id, ignores); err != nil {
		return fmt.Errorf("couldn't set which files sync: %w", err)
	}
	_ = c.Rescan(ctx, p.id)
	expected := map[string]bool{} // local changes the snapshot covers
	for _, rel := range append(append(append([]string{}, p.diff.Changed...), p.diff.Removed...), p.replaces...) {
		expected[strings.ToLower(rel)] = true
	}
	reverts := 0
	for {
		st, err := c.FolderStatus(ctx, p.id)
		switch {
		case err != nil || st.State != "idle":
		case st.ReceiveOnlyTotalItems > 0:
			names, err := c.LocalChanged(ctx, p.id)
			if err != nil {
				return fmt.Errorf("couldn't list the files changed here: %w", err)
			}
			for _, n := range names {
				rel := filepath.ToSlash(n)
				if fi, err := os.Lstat(filepath.Join(p.path, n)); err == nil && fi.IsDir() {
					continue // a folder the mod files are in
				}
				if !expected[strings.ToLower(rel)] {
					return fmt.Errorf("%s's files don't match its list (%s): not put back", p.src.Name, rel)
				}
			}
			if reverts++; reverts > 5 {
				return errors.New("files keep changing here during the update")
			}
			if err := c.Revert(ctx, p.id); err != nil {
				return fmt.Errorf("couldn't put back files changed here: %w", err)
			}
		case st.NeedFiles == 0 && st.NeedBytes == 0 && arrived(p):
			return nil
		}
		if err := sleepCtx(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// checkIndex waits for the source's file list and compares it with the
// inventory. After resuming, this PC may still hold the list from before,
// so a match only counts once it has held for a while with the source
// connected, and a difference once it has lasted a while.
func checkIndex(ctx context.Context, c *syncthing.Client, p *applyPlan) error {
	settle := time.Now().Add(90 * time.Second)
	var matchSince time.Time
	for {
		if conns, err := c.Connections(ctx); err != nil || !conns.Connections[p.src.Device].Connected {
			matchSince = time.Time{}
			if time.Now().After(settle) {
				return fmt.Errorf("%s isn't connected", p.src.Name)
			}
			if err := sleepCtx(ctx, 3*time.Second); err != nil {
				return err
			}
			continue
		}
		problem, waiting := "", false
		for _, f := range p.inv.Files {
			g, err := c.Global(ctx, p.id, f.Rel)
			switch {
			case err != nil:
				return fmt.Errorf("couldn't read %s's file list: %w", p.src.Name, err)
			case !g.Exists:
				waiting, problem = true, f.Rel+" is missing"
			case g.Deleted:
				problem = f.Rel + " is deleted there"
			case g.Size != f.Size:
				problem = f.Rel + " has another size there"
			}
			if problem != "" {
				break
			}
		}
		if problem == "" {
			for _, rel := range p.diff.Removed {
				if g, err := c.Global(ctx, p.id, rel); err == nil && g.Exists && !g.Deleted {
					problem = rel + " is still there"
					break
				}
			}
		}
		if problem == "" {
			if matchSince.IsZero() {
				matchSince = time.Now()
			}
			if st, err := c.FolderStatus(ctx, p.id); err == nil && st.State == "idle" && time.Since(matchSince) >= 10*time.Second {
				return nil
			}
			if err := sleepCtx(ctx, 3*time.Second); err != nil {
				return err
			}
			continue
		}
		matchSince = time.Time{}
		if time.Now().After(settle) {
			if waiting {
				return fmt.Errorf("%s's file list didn't arrive (is it on?): %s", p.src.Name, problem)
			}
			return fmt.Errorf("%s's files don't match its list: %s; nothing was changed", p.src.Name, problem)
		}
		if err := sleepCtx(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return errors.New("the update didn't finish in time (is the source PC still on?)")
	case <-time.After(d):
		return nil
	}
}

// arrived is a quick check (sizes, and hashes of the files that changed)
// that every file of the update is here.
func arrived(p *applyPlan) bool {
	changed := map[string]bool{}
	for _, rel := range append(append([]string{}, p.diff.Changed...), p.replaces...) {
		changed[strings.ToLower(rel)] = true
	}
	for _, f := range p.inv.Files {
		fp := filepath.Join(p.path, filepath.FromSlash(f.Rel))
		fi, err := os.Lstat(fp)
		if err != nil || fi.Size() != f.Size {
			return false
		}
		if changed[strings.ToLower(f.Rel)] && f.SHA256 != "" {
			if h, err := mods.HashFile(fp); err != nil || h != f.SHA256 {
				return false
			}
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

// ---- rollback and audits ----------------------------------------------------------

// SnapshotView is a snapshot, for the UI.
type SnapshotView struct {
	Stamp   string    `json:"stamp"`
	Created time.Time `json:"created"`
	Files   int       `json:"files"`
	Added   int       `json:"added"`
	Bytes   int64     `json:"bytes"`
}

// ModSnapshots lists the snapshots taken before updates of a folder, newest
// first. Only the newest can be rolled back to: undoing an older update
// would leave the files of the ones after it behind.
func (a *App) ModSnapshots(id string) []SnapshotView {
	var out []SnapshotView
	for _, sn := range mods.Snapshots(id) {
		out = append(out, SnapshotView{Stamp: sn.Stamp, Created: sn.Created, Files: len(sn.Copied), Added: len(sn.Added), Bytes: sn.Bytes})
	}
	return out
}

// RollbackMods puts a deployed-mods folder back the way it was before its
// last update: the files it replaced return and the files it added go.
func (a *App) RollbackMods(id, stamp string) error {
	s := store.LoadSettings()
	mf, ok := s.Mods[id]
	if !ok || mf.Kind != mods.KindDeployed || mf.Role == meta.RoleSource {
		return errors.New("not a folder receiving deployed mods")
	}
	if applying.has(id) {
		return errApplying
	}
	procs := processPaths()
	gameDir := mods.GameDir(mf.Game)
	if mods.ManagerRunning(procs) || gameRunning(gameDir, procs) {
		return errors.New("close Vortex and the game first")
	}
	sns := mods.Snapshots(id)
	if len(sns) == 0 || sns[0].Stamp != stamp {
		return errors.New("only the last update can be rolled back")
	}
	sn := sns[0]
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
	scope := mods.Scope(prev, loadInv(id, "target"), loadInv(id, "applied"))
	outside, err := mods.Outside(f.Path, scope)
	if err != nil {
		return err
	}
	rerr := mods.Rollback(sn, f.Path)
	checks := []mods.Check{{Name: "Files put back", OK: rerr == nil, Detail: errText(rerr)}}
	if prev != nil {
		checks = append(checks, mods.CheckApplied(f.Path, *prev, nil)...)
	}
	if after, err := mods.Outside(f.Path, scope); err == nil {
		checks = append(checks, mods.CheckOutside(outside, after))
	}
	ok = mods.Passed(checks)
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "rollback", Gen: sn.Gen, Checks: checks,
		Summary: fmt.Sprintf("Rolled back the update of %s", sn.Created.Local().Format("2 Jan 15:04"))})
	if !ok {
		holdMods(ctx, c, id, heldCheck, "the rollback failed its checks: "+strings.Join(mods.Failed(checks), "; "))
		runtime.EventsEmit(a.ctx, "changed")
		return errors.New("rolled back, but some checks failed: see the audit")
	}
	_ = saveInv(id, "applied", prev)
	_ = saveInv(id, "target", nil)
	_ = os.RemoveAll(mods.SnapshotDir(id, sn.Stamp)) // undone: the one before it is the last now
	updateModSync(id, func(m *store.ModSyncState) {
		m.Phase, m.Held, m.HeldBy, m.AppliedGen, m.LastAudit = phaseIdle, "", "", sn.Gen, time.Now()
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
	st, err := c.Status(ctx)
	if err != nil {
		return mods.AuditEntry{}, err
	}
	var inv *mods.Inventory
	wantType := "receiveonly"
	if mf.Role == meta.RoleSource {
		wantType = "sendonly"
		if ss := loadSource(id); ss != nil {
			inv = &ss.Inv
		}
	} else {
		inv = loadInv(id, "applied")
	}
	e := mods.AuditEntry{Folder: id, Label: cmpOr(f.Label, id), Phase: "check"}
	e.Checks = append(e.Checks, mods.Check{Name: "Folder type", OK: f.Type == wantType, Detail: f.Type})
	if mf.Role != meta.RoleSource {
		src := store.LoadState().ModSync[id].Source
		e.Checks = append(e.Checks, mods.Check{Name: "Shared with the source only", OK: sharedOnlyWith(f, st.MyID, src)})
	}
	if inv == nil {
		e.Summary = "Nothing applied yet"
		e.Checks = append(e.Checks, mods.Check{Name: "Update applied", OK: true, Warn: true, Detail: "none yet"})
	} else {
		e.Gen = inv.Gen
		e.Checks = append(e.Checks, mods.CheckApplied(f.Path, *inv, nil)...)
		if pc, ok := mods.CheckPlugins(mf.Game, f.Path); ok {
			e.Checks = append(e.Checks, pc)
		}
		e.Summary = fmt.Sprintf("Checked %d mod files", len(inv.Files))
	}
	mods.LogAudit(e)
	e.OK = mods.Passed(e.Checks)
	updateModSync(id, func(m *store.ModSyncState) { m.LastAudit = time.Now() })
	return e, nil
}

// ReleaseModHold lets a held folder go on (after the user looked at why it
// was held). With a handover waiting it keeps this PC's source: on the
// source it keeps sending (and claims the role anew); on a receiver it takes
// updates from the PC that claims to send them now.
func (a *App) ReleaseModHold(id string) error {
	if applying.has(id) {
		return errApplying
	}
	ms := store.LoadState().ModSync[id]
	mf := store.LoadSettings().Mods[id]
	if !store.LoadSettings().SyncDeployedMods {
		return errors.New(`turn on "Sync deployed mods in the game folder" in Settings first`)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	if mf.Role == meta.RoleSource {
		if ms.HeldBy == heldHandover {
			updateModSync(id, func(m *store.ModSyncState) { m.Since, m.Handover = time.Now().UnixNano(), "" })
			_, _ = meta.Reconcile(ctx, c)
		}
		resumeSource(ctx, c, id) // held again at once if it still can't be read
	} else {
		if ms.HeldBy == heldHandover && ms.Handover != "" {
			if err := a.followSource(ctx, c, id, ms.Handover); err != nil {
				return err
			}
		}
		updateModSync(id, func(m *store.ModSyncState) { m.Phase, m.Held, m.HeldBy, m.Handover = phasePending, "", "", "" })
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// followSource makes a receiver take updates from device from now on.
func (a *App) followSource(ctx context.Context, c *syncthing.Client, id, device string) error {
	src, ok := meta.ModSource(id)
	if !ok || src.Device != device {
		return errors.New("that PC no longer claims to send these mods")
	}
	if err := meta.WrittenBy(ctx, c, device); err != nil {
		return fmt.Errorf("can't confirm %s sends these mods: %w", src.Name, err)
	}
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if err := c.PatchFolder(ctx, id, map[string]any{"paused": true, "devices": devicesFor(st.MyID, []string{device})}); err != nil {
		return err
	}
	updateModSync(id, func(m *store.ModSyncState) { m.Source = device })
	mods.LogAudit(mods.AuditEntry{Folder: id, Label: id, Phase: "source",
		Summary: "Updates come from " + src.Name + " now", Checks: []mods.Check{{Name: "Source changed", OK: true, Detail: src.Name}}})
	return nil
}

// deployedFiles are the mod files of a deployed-mods folder on this PC: the
// deployment it sends, or the last update it applied (none yet: nothing).
func deployedFiles(s store.Settings, id string) []string {
	var inv *mods.Inventory
	if s.Mods[id].Role == meta.RoleSource {
		if ss := loadSource(id); ss != nil {
			inv = &ss.Inv
		}
	} else {
		inv = loadInv(id, "applied")
	}
	if inv == nil {
		return nil
	}
	out := make([]string, 0, len(inv.Files))
	for _, f := range inv.Files {
		out = append(out, f.Rel)
	}
	return out
}

// ---- view -------------------------------------------------------------------------

// modView fills in a deployed-mods folder's update state.
func (a *App) modView(v *FolderView) {
	if v.Kind != mods.KindDeployed {
		return
	}
	ms := store.LoadState().ModSync[v.ID]
	v.ModPhase, v.ModHeld, v.ModHeldBy = cmpOr(ms.Phase, phaseIdle), ms.Held, ms.HeldBy
	if v.ModRole == meta.RoleSource {
		if ss := loadSource(v.ID); ss != nil {
			if ss.Inv.Busy && ms.Phase != phaseHeld {
				v.ModPhase = phaseBusy
			}
			v.ModPending = fmt.Sprintf("%d files", len(ss.Inv.Files))
		}
		return
	}
	if ms.Source == "" {
		v.ModPending = "no PC sends these mods"
		return
	}
	name := meta.Peers()[ms.Source]
	if inv, ok := meta.ModInventories(ms.Source)[v.ID]; ok && inv.Gen > ms.AppliedGen {
		v.ModPending = "update from " + cmpOr(name, "the source")
		if v.ModPhase == phaseIdle {
			v.ModPhase = phasePending
		}
	} else {
		v.ModPending = "from " + cmpOr(name, "the source")
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
