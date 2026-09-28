package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Sharing Vortex's mod list (experimental) lets Vortex on every PC manage
// the same synced mods. Syncing the staging folder only brings the mod
// files: each PC's Vortex keeps its own list of mods (with their name,
// version, Nexus ids) and which ones are enabled, so a mod arriving from
// another PC shows up as an unknown folder, without its details, disabled.
//
// With the option on, each PC whose staging folder of a game is synced
// reads that game's mod list from Vortex's database while Vortex is closed
// and publishes it in the shared metadata (<device>.vortexmods). Every mod
// carries the time this PC last saw it change; for each mod the newest
// version among all PCs wins and is written into this PC's Vortex:
//
//   - a mod installed or changed elsewhere is added to Vortex here once its
//     folder has fully arrived in the staging folder, and enabled or
//     disabled like there;
//   - a mod removed elsewhere leaves Vortex here once its folder is gone;
//   - nothing else in Vortex's database is touched, and a copy of it is
//     kept (in %LOCALAPPDATA%\Syncer\vortex-state) before each change.
//
// Each PC still deploys with its own Vortex: open it and deploy after
// changes arrive. When sharing starts, what each PC already has counts as
// old, so nothing is undone: mods only one PC has are added to the others.

// vortexShareState is what this PC keeps about the shared mod lists.
type vortexShareState struct {
	// List is what this PC publishes.
	List mods.ShareList `json:"list,omitempty"`
	// DBStamp and Peers are what the last look was based on: nothing
	// changed since, nothing needs a look.
	DBStamp string            `json:"dbStamp,omitempty"`
	Peers   map[string]string `json:"peers,omitempty"`
	// Push are games whose list this PC hands to the others as it is.
	Push   map[string]bool             `json:"push,omitempty"`
	Status map[string]vortexGameStatus `json:"status,omitempty"`
}

type vortexGameStatus struct {
	Checked  time.Time `json:"checked"`
	Applied  time.Time `json:"applied,omitempty"`  // last time changes from other PCs were written
	AppliedN int       `json:"appliedN,omitempty"` // … and how many mods they touched
	Waiting  int       `json:"waiting,omitempty"`  // mods from other PCs waiting for their files
	Err      string    `json:"err,omitempty"`
}

func vortexSharePath() string { return filepath.Join(modStateDir(), "vortex-share.json") }

func loadVortexShare() vortexShareState {
	var st vortexShareState
	loadJSON(vortexSharePath(), &st)
	if st.List == nil {
		st.List = mods.ShareList{}
	}
	if st.Status == nil {
		st.Status = map[string]vortexGameStatus{}
	}
	return st
}

// goneAge is how long a removed mod stays in the list, so the removal
// reaches PCs that are off for a while.
const goneAge = 60 * 24 * time.Hour

// shareGames are the games whose staging folder is synced here (game ->
// staging folder), with its Syncthing folder id.
func shareGames(s store.Settings, fs []syncthing.Folder) map[string][2]string {
	byID := map[string]syncthing.Folder{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	out := map[string][2]string{}
	for id, mf := range s.Mods {
		f, ok := byID[id]
		if mf.Kind != mods.KindStaging || !ok {
			continue
		}
		out[mf.Game] = [2]string{f.Path, id}
	}
	return out
}

var vortexShareMu sync.Mutex

// recheckEvery is how often mods waiting for their files are looked at
// again when nothing else changed.
const recheckEvery = 3 * time.Minute

// vortexShareTick runs from modsTick while Vortex is closed. It reports
// whether it changed anything.
func vortexShareTick(ctx context.Context, c *syncthing.Client, s store.Settings, fs []syncthing.Folder) bool {
	vortexShareMu.Lock()
	defer vortexShareMu.Unlock()
	st, err := c.Status(ctx)
	if err != nil {
		return false
	}
	me := st.MyID
	if !s.ShareVortexMods {
		if meta.VortexListStamp(me) != "" {
			_ = meta.WriteVortexLists(me, nil)
			_ = os.Remove(vortexSharePath())
			return true
		}
		return false
	}
	games := shareGames(s, fs)
	state := loadVortexShare()
	peers := map[string]string{}
	for dev := range meta.Peers() {
		if dev != me {
			if ps := meta.VortexListStamp(dev); ps != "" {
				peers[dev] = ps
			}
		}
	}
	// Mods waiting for their files (or an error) are looked at again now
	// and then, not on every round: each look opens Vortex's database.
	waiting := false
	for g := range games {
		st := state.Status[g]
		waiting = waiting || (st.Waiting > 0 || st.Err != "") && time.Since(st.Checked) > recheckEvery
	}
	dbStamp := mods.VortexDBStamp()
	if dbStamp == "" {
		return false // Vortex isn't used on this PC
	}
	if !waiting && len(state.Push) == 0 && dbStamp == state.DBStamp && sameStamps(peers, state.Peers) && sameGames(state.List, games) {
		return false
	}

	// Lists from other PCs, only from the PC that wrote them.
	lists := map[string]mods.ShareList{}
	for dev := range peers {
		if err := meta.VortexListWrittenBy(ctx, c, dev); err != nil {
			warnOnceMods("vortexlist:"+dev, "mods: ignoring the Vortex mod list of %s: %v", dev, err)
			continue
		}
		if l := meta.VortexLists(dev); len(l) > 0 {
			lists[dev] = l
		}
	}

	staging := map[string]string{}
	for g, sf := range games {
		staging[g] = sf[0]
	}
	complete := func(g string) bool {
		fst, err := c.FolderStatus(ctx, games[g][1])
		return err == nil && fst.NeedFiles == 0 && fst.NeedBytes == 0
	}
	var changed bool
	state, changed, err = shareAll(staging, lists, complete)
	if err != nil {
		if !errors.Is(err, mods.ErrVortexOpen) {
			warnOnceMods("vortexdb", "mods: %v", err)
			for g := range games {
				setShareErr(&state, g, err.Error())
			}
			_ = store.WriteJSON(vortexSharePath(), state)
		}
		return false
	}
	for g := range state.List {
		if _, ok := games[g]; !ok {
			delete(state.List, g) // no longer synced here
			delete(state.Status, g)
		}
	}
	state.Push = nil
	state.DBStamp, state.Peers = mods.VortexDBStamp(), peers
	old := meta.VortexLists(me)
	if err := meta.WriteVortexLists(me, state.List); err != nil {
		logx.Printf("mods: publish Vortex mod lists: %v", err)
	}
	if err := store.WriteJSON(vortexSharePath(), state); err != nil {
		logx.Printf("mods: %v", err)
	}
	return changed || !sameLists(old, state.List)
}

// errNeedWrite: a game's mod list needs changes in Vortex's database.
var errNeedWrite = errors.New("changes to write")

// shareAll brings the mod lists of games (id -> staging folder) in line
// with Vortex's database. It first only looks; when something is to be
// written, it closes the database, saves a copy of it (nothing changes it
// while it's closed and Vortex isn't running), opens it again and writes.
// It returns the updated state and whether Vortex was changed.
func shareAll(games map[string]string, lists map[string]mods.ShareList, complete func(game string) bool) (vortexShareState, bool, error) {
	db, err := mods.OpenVortexDB()
	if err != nil {
		return loadVortexShare(), false, err
	}
	// Only one process looks at a time: the database is open exclusively.
	state := loadVortexShare()
	run := func(write bool) (need, changed bool) {
		for _, g := range sortedKeys(games) {
			n, err := shareGame(db, &state, g, games[g], lists, func() bool { return complete(g) }, write)
			if errors.Is(err, errNeedWrite) {
				need = true
				continue
			}
			stt := state.Status[g]
			stt.Checked, stt.Err = time.Now(), ""
			if err != nil {
				stt.Err = err.Error()
				logx.Printf("mods: sharing the Vortex mod list of %s: %v", mods.NameOf(g), err)
			}
			if n > 0 {
				stt.Applied, stt.AppliedN = time.Now(), n
				changed = true
				logx.Printf("mods: updated %d mod(s) of %s in Vortex from your other PCs", n, mods.NameOf(g))
			}
			state.Status[g] = stt
		}
		return need, changed
	}
	need, _ := run(false)
	_ = db.Close()
	if !need {
		return state, false, nil
	}
	if mods.ManagerRunning(processPaths()) {
		return state, false, mods.ErrVortexOpen
	}
	p, err := mods.BackupVortexDB()
	if err != nil {
		return state, false, fmt.Errorf("couldn't save a copy of Vortex's database first: %w", err)
	}
	logx.Printf("mods: saved a copy of Vortex's database in %s", p)
	if db, err = mods.OpenVortexDB(); err != nil {
		return state, false, err
	}
	_, changed := run(true)
	return state, changed, db.Close()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setShareErr(st *vortexShareState, g, msg string) {
	s := st.Status[g]
	s.Checked, s.Err = time.Now(), msg
	st.Status[g] = s
}

// shareGame brings one game's mod list in line; it returns how many mods
// it changed in Vortex here. complete reports whether the staging folder
// has everything other PCs sent. Unless it may write, it returns
// errNeedWrite (and changes nothing) when Vortex's database needs changes.
func shareGame(db *mods.VortexDB, state *vortexShareState, g, staging string,
	lists map[string]mods.ShareList, complete func() bool, write bool) (int, error) {
	gm, err := db.Mods(g)
	if err != nil {
		return 0, err
	}
	if gm.Profile == "" {
		return 0, errors.New("Vortex has no profile for this game here yet: manage the game in Vortex once")
	}
	mine := state.List[g]
	first := mine == nil
	now := time.Now().UnixNano()

	// The newest version of each mod on the other PCs.
	best := map[string]mods.ShareEntry{}
	for _, l := range lists {
		for id, e := range l[g] {
			if b, ok := best[id]; !ok || e.At > b.At {
				best[id] = e
			}
		}
	}

	// What changed in Vortex here since the last look.
	next := map[string]mods.ShareEntry{}
	for id, e := range mine {
		next[id] = e
	}
	for id, m := range gm.Mods {
		if !m.Installed() || m.Check(id) != nil {
			continue
		}
		h := m.Hash()
		e, known := mine[id]
		if known && !e.Gone && e.Hash == h && !state.Push[g] {
			continue
		}
		if !known && !first && !state.Push[g] {
			if b, ok := best[id]; ok && !b.Gone {
				// Vortex added a mod that arrived from another PC by itself
				// (its folder showed up while it was open), without its
				// details: the other PC's version wins.
				continue
			}
		}
		at := now
		if first && !state.Push[g] {
			at = 1 // what was here when sharing started counts as old
		}
		mc := m
		next[id] = mods.ShareEntry{At: at, Hash: h, Mod: &mc}
	}
	for id, e := range mine {
		if _, ok := gm.Mods[id]; !e.Gone && !ok {
			next[id] = mods.ShareEntry{At: now, Gone: true}
		}
	}

	// Take the newer versions from the other PCs.
	var changes []mods.VortexChange
	waiting := 0
	arrived := -1 // whether the staging folder has everything (-1: not asked yet)
	for id, b := range best {
		e, has := next[id]
		if has && e.At >= b.At {
			continue
		}
		local, here := gm.Mods[id]
		if b.Gone {
			if here {
				if dirExists(filepath.Join(staging, local.Folder(id))) {
					waiting++ // its removal hasn't arrived yet
					continue
				}
				changes = append(changes, mods.VortexChange{ID: id})
			}
			next[id] = b
			continue
		}
		if here && local.Hash() == b.Hash {
			next[id] = b
			continue
		}
		if !dirExists(filepath.Join(staging, b.Mod.Folder(id))) {
			waiting++
			continue
		}
		if arrived < 0 {
			arrived = 0
			if complete() {
				arrived = 1
			}
		}
		if arrived == 0 {
			waiting++ // mods are still arriving
			continue
		}
		changes = append(changes, mods.VortexChange{ID: id, Mod: b.Mod})
		next[id] = b
	}
	for id, e := range next {
		if e.Gone && time.Since(time.Unix(0, e.At)) > goneAge {
			delete(next, id)
		}
	}
	if len(changes) > 0 {
		if !write {
			return 0, errNeedWrite
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
		if err := db.Apply(g, gm.Profile, changes); err != nil {
			return 0, err
		}
	}
	state.List[g] = next
	s := state.Status[g]
	s.Waiting = waiting
	state.Status[g] = s
	return len(changes), nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func sameStamps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// sameGames reports whether the list covers exactly the games synced here.
func sameGames(l mods.ShareList, games map[string][2]string) bool {
	if len(l) != len(games) {
		return false
	}
	for g := range games {
		if _, ok := l[g]; !ok {
			return false
		}
	}
	return true
}

func sameLists(a, b mods.ShareList) bool {
	if len(a) != len(b) {
		return false
	}
	for g, am := range a {
		bm, ok := b[g]
		if !ok || len(am) != len(bm) {
			return false
		}
		for id, e := range am {
			f, ok := bm[id]
			if !ok || f.At != e.At || f.Gone != e.Gone || f.Hash != e.Hash {
				return false
			}
		}
	}
	return true
}

// ---- for the Mods page ----------------------------------------------------------

// VortexShareView is how sharing one game's Vortex mod list goes.
type VortexShareView struct {
	Game     string    `json:"game"`
	Name     string    `json:"name"`
	Mods     int       `json:"mods"`    // mods in the shared list
	Enabled  int       `json:"enabled"` // … enabled here
	Waiting  int       `json:"waiting"` // mods from other PCs waiting for their files
	Checked  time.Time `json:"checked"`
	Applied  time.Time `json:"applied"`
	AppliedN int       `json:"appliedN"`
	Err      string    `json:"err"`
	Peers    []string  `json:"peers"` // the other PCs sharing this game's list
	Pushing  bool      `json:"pushing"`
}

// VortexShares lists how sharing each game's Vortex mod list goes on this
// PC (nothing while the experimental option is off).
func (a *App) VortexShares() []VortexShareView {
	s := store.LoadSettings()
	if !s.ShareVortexMods {
		return nil
	}
	state := loadVortexShare()
	names := meta.Peers()
	me := ""
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		if st, err := c.Status(ctx); err == nil {
			me = st.MyID
		}
		cancel()
	}
	games := map[string]bool{}
	for _, mf := range s.Mods {
		if mf.Kind == mods.KindStaging {
			games[mf.Game] = true
		}
	}
	out := []VortexShareView{}
	for g := range games {
		v := VortexShareView{Game: g, Name: mods.NameOf(g), Pushing: state.Push[g], Peers: []string{}}
		for _, e := range state.List[g] {
			if !e.Gone {
				v.Mods++
				if e.Mod != nil && e.Mod.Enabled {
					v.Enabled++
				}
			}
		}
		st := state.Status[g]
		v.Waiting, v.Checked, v.Applied, v.AppliedN, v.Err = st.Waiting, st.Checked, st.Applied, st.AppliedN, st.Err
		for dev, name := range names {
			if dev != me && len(meta.VortexLists(dev)[g]) > 0 {
				v.Peers = append(v.Peers, cmpOr(name, dev[:min(7, len(dev))]))
			}
		}
		sort.Strings(v.Peers)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(mods.NameOf(out[i].Game)) < strings.ToLower(mods.NameOf(out[j].Game))
	})
	return out
}

// PushVortexList makes this PC's Vortex mod list of a game the newest, so
// the other PCs take it as it is (which mods are enabled, their details).
// It happens the next time Vortex is closed here.
func (a *App) PushVortexList(game string) error {
	s := store.LoadSettings()
	if !s.ShareVortexMods {
		return errors.New(`turn on "Manage synced mods with Vortex on every PC" in Settings first`)
	}
	found := false
	for _, mf := range s.Mods {
		found = found || mf.Kind == mods.KindStaging && mf.Game == game
	}
	if !found {
		return errors.New("this game's Vortex mods aren't synced on this PC")
	}
	vortexShareMu.Lock()
	state := loadVortexShare()
	if state.Push == nil {
		state.Push = map[string]bool{}
	}
	state.Push[game] = true
	err := store.WriteJSON(vortexSharePath(), state)
	vortexShareMu.Unlock()
	if err != nil {
		return err
	}
	go a.kickMods()
	return nil
}

// kickMods runs modsTick now instead of on its next round.
func (a *App) kickMods() {
	c, err := a.client()
	if err != nil {
		return
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	if modsTick(ctx, c) && a.ctx != nil {
		runtime.EventsEmit(a.ctx, "changed")
	}
}
