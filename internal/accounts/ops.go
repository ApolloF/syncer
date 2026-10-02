package accounts

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

	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Every operation that moves saves (split, switch, merge) is written to a
// journal before each step, and every step can run again: if Syncer stops
// halfway (crash, power cut, sign-out), the next start finishes the
// operation. Nothing is ever deleted: replaced files go to the backup
// history first, and folders that must make room go to the vault's .trash.

// Syncthing is the part of the Syncthing client the operations use.
type Syncthing interface {
	Folders(ctx context.Context) ([]syncthing.Folder, error)
	PatchFolder(ctx context.Context, id string, patch map[string]any) error
	RemoveFolder(ctx context.Context, id string) error
	FolderStatus(ctx context.Context, id string) (syncthing.FolderStatus, error)
}

// Env is what the operations need from the rest of Syncer.
type Env struct {
	ST Syncthing
	Me string // this PC's device id
	// AddFolder starts syncing a folder with every paired PC. It must
	// protect files already at path (a pinned restore point) first.
	AddFolder func(ctx context.Context, id, label, path string) error
	// Snapshot saves the files at path as a pinned restore point of id.
	Snapshot func(ctx context.Context, id, label, path string) error
	// Keep moves one file into id's history (restorable as rel).
	Keep func(id, abs, rel string) error
	// Protected notes that the files at path, unless changed since, were
	// saved as a restore point at the given time, so AddFolder doesn't save
	// them all again (nil: it does).
	Protected func(id, path string, at time.Time)
	// CopyHistory seeds to's backup history from from's (best effort). It
	// may finish after the operation returns.
	CopyHistory func(ctx context.Context, from, to string)
	// Busy refuses while a game is running (nil: no check).
	Busy func() error
	// HoldBackups waits for a running backup and keeps new ones from
	// starting while saves move (nil: no lock).
	HoldBackups func(ctx context.Context) (release func(), err error)
	// NoSplit refuses to split a folder that must stay whole (a launcher's
	// own data), even when another PC split it (nil: no check).
	NoSplit func(game string) error
}

func (env Env) hold(ctx context.Context) (func(), error) {
	if env.HoldBackups == nil {
		return func() {}, nil
	}
	return env.HoldBackups(ctx)
}

// Journal is the operation in progress.
type Journal struct {
	Kind   string          `json:"kind"` // split | merge | switch
	Record Record          `json:"record"`
	Live   string          `json:"live,omitempty"`  // split/merge: the save folder
	Place  string          `json:"place,omitempty"` // split: the account whose saves stay at Live
	To     string          `json:"to,omitempty"`    // switch: the account switched to
	Step   int             `json:"step"`
	Built  map[string]bool `json:"built,omitempty"` // split: accounts whose vault is complete
	Moves  []Move          `json:"moves,omitempty"` // switch/merge: folders being swapped
	// WasPaused: folders that were already paused before this operation
	// paused them (e.g. by "Pause syncing"); they stay paused afterwards.
	WasPaused map[string]bool `json:"wasPaused,omitempty"`
	// EmptyLive: a merge on a PC without the winner's saves starts the
	// shared folder empty (the other PCs fill it) instead of with the
	// saves of whoever was playing here.
	EmptyLive bool      `json:"emptyLive,omitempty"`
	Started   time.Time `json:"started"`
	Error     string    `json:"error,omitempty"` // why it stopped last time
}

// Move swaps the folder at a game's save path for another account's.
type Move struct {
	Game  string `json:"game"`
	Live  string `json:"live"`  // the game's save folder
	Hold  string `json:"hold"`  // where the outgoing saves wait during the swap
	InID  string `json:"inID"`  // folder id coming to Live
	In    string `json:"in"`    // … and where it is now
	OutID string `json:"outID"` // folder id leaving Live
	Out   string `json:"out"`   // … and where it goes
	Step  int    `json:"step"`
}

func journalFile() string { return filepath.Join(dir(), "accounts-op.json") }

// PendingOp returns the unfinished operation, if any.
func PendingOp() (Journal, bool) {
	b, err := os.ReadFile(journalFile())
	if err != nil {
		return Journal{}, false
	}
	var j Journal
	if json.Unmarshal(b, &j) != nil || j.Kind == "" {
		return Journal{}, false
	}
	return j, true
}

func (j *Journal) save() error {
	if err := store.WriteJSON(journalFile(), j); err != nil {
		return err
	}
	if crashHook != nil {
		crashHook(j)
	}
	return nil
}

// crashHook lets tests stop an operation right after a journal write, as if
// Syncer had been killed there.
var crashHook func(*Journal)

func clearJournal() error {
	if err := os.Remove(journalFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var opMu sync.Mutex

// lockOps allows one operation at a time, across Syncer's processes.
func lockOps() func() {
	opMu.Lock()
	unlock := store.FileLock("accounts-op")
	return func() {
		unlock()
		opMu.Unlock()
	}
}

// failAt records why an operation stopped, for the UI and the next attempt.
func (j *Journal) failAt(err error) error {
	j.Error = err.Error()
	_ = j.save()
	return err
}

// ---- running and resuming ------------------------------------------------------

// Apply finishes an interrupted operation, then carries out the split and
// merge records other PCs (or this one) made that this PC hasn't yet. It
// returns whether folders changed.
func Apply(ctx context.Context, env Env) (bool, error) {
	unlock := lockOps()
	defer unlock()
	changed := false
	if j, ok := PendingOp(); ok {
		if err := run(ctx, env, &j); err != nil {
			return false, err
		}
		changed = true
	}
	// The account playing here needs its own folder in every split game
	// (it may have been created while another PC split a game).
	if env.Me != "" {
		if _, err := Update(func(s *State) error {
			s.EnsureMember(s.ActiveID(), env.Me, time.Now())
			return nil
		}); err != nil {
			return changed, err
		}
	}
	s := Load()
	s.Clean()
	var errs []error
	for _, r := range s.Pending() {
		if r.Kind == KindSplit && env.NoSplit != nil {
			if err := env.NoSplit(r.Game); err != nil {
				noteError(r.Game, err)
				errs = append(errs, fmt.Errorf("%s: %w", r.Label, err))
				continue
			}
		}
		j := Journal{Kind: r.Kind, Record: r, Started: time.Now()}
		err := run(ctx, env, &j)
		noteError(r.Game, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Label, err))
			if _, stuck := PendingOp(); stuck {
				return changed, errors.Join(errs...) // finish that one first
			}
			continue
		}
		changed = true
	}
	// Every split game's saves at its save folder should be the active
	// account's (a PC that split while its account had no saves yet, or a
	// switch that couldn't finish).
	if moved, err := converge(ctx, env); err != nil {
		errs = append(errs, err)
	} else if moved {
		changed = true
	}
	return changed, errors.Join(errs...)
}

// noteError remembers why a game's record couldn't be carried out yet.
func noteError(game string, err error) {
	_, _ = Update(func(s *State) error {
		if err == nil {
			delete(s.Errors, game)
		} else {
			s.Errors[game] = err.Error()
		}
		return nil
	})
}

// converge puts the active account's saves in place where they aren't.
// It quietly waits while they haven't reached this PC.
func converge(ctx context.Context, env Env) (bool, error) {
	active := Load().ActiveID()
	if active == "" {
		return false, nil
	}
	moves, _, err := planMoves(ctx, env, active) // games whose saves haven't arrived wait
	if err != nil || len(moves) == 0 {
		return false, nil
	}
	return true, switchLocked(ctx, env, active, moves)
}

// catchUp is how long a PC that didn't make a split waits for the game's
// shared folder to finish syncing before it splits anyway: splitting with
// the same saves as the PC that decided gives identical folders.
const catchUp = time.Hour

// ErrWaiting means the operation waits for Syncthing and is retried later.
var ErrWaiting = errors.New("waiting for the saves to finish syncing first")

func run(ctx context.Context, env Env, j *Journal) error {
	var err error
	switch j.Kind {
	case KindSplit:
		err = runSplit(ctx, env, j)
	case KindMerge:
		err = runMerge(ctx, env, j)
	case "switch":
		err = runSwitch(ctx, env, j)
	default:
		err = clearJournal()
	}
	if err != nil {
		return err
	}
	if j.Kind == KindSplit || j.Kind == KindMerge {
		if _, err := Update(func(s *State) error {
			s.Applied[j.Record.Game] = j.Record.Stamp()
			return nil
		}); err != nil {
			return err
		}
	}
	return clearJournal()
}

type folderSet map[string]syncthing.Folder

func folders(ctx context.Context, st Syncthing) (folderSet, error) {
	fs, err := st.Folders(ctx)
	if err != nil {
		return nil, err
	}
	m := folderSet{}
	for _, f := range fs {
		m[f.ID] = f
	}
	return m, nil
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// atLive finds the account folder of game that sits at live.
func (fs folderSet) atLive(game, live string) (syncthing.Folder, string, bool) {
	for id, f := range fs {
		if g, a, ok := ParseFolderID(id); ok && g == game && samePath(f.Path, live) {
			return f, a, true
		}
	}
	return syncthing.Folder{}, "", false
}

// pause stops Syncthing on folders while their files move, remembering
// which were paused already.
func (j *Journal) pause(ctx context.Context, st Syncthing, ids ...string) error {
	fs, err := folders(ctx, st)
	if err != nil {
		return err
	}
	if j.WasPaused == nil {
		j.WasPaused = map[string]bool{}
	}
	var todo []string
	for _, id := range ids {
		f, ok := fs[id]
		if !ok {
			continue
		}
		if _, seen := j.WasPaused[id]; !seen {
			j.WasPaused[id] = f.Paused
		}
		if !f.Paused {
			todo = append(todo, id)
		}
	}
	if err := j.save(); err != nil {
		return err
	}
	for _, id := range todo {
		if err := st.PatchFolder(ctx, id, map[string]any{"paused": true}); err != nil {
			return err
		}
	}
	return nil
}

// resume lets Syncthing work on folders again, except those that were
// paused before the operation.
func (j *Journal) resume(ctx context.Context, st Syncthing, ids ...string) error {
	if store.LoadSettings().Paused() {
		// "Pause syncing" started meanwhile: these resume when it ends.
		store.UpdateState(func(s *store.State) {
			for _, id := range ids {
				if !j.WasPaused[id] && !slices.Contains(s.PausedFolders, id) {
					s.PausedFolders = append(s.PausedFolders, id)
				}
			}
		})
		return nil
	}
	var errs []error
	for _, id := range ids {
		if j.WasPaused[id] {
			continue
		}
		if err := st.PatchFolder(ctx, id, map[string]any{"paused": false}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- split ---------------------------------------------------------------------

const (
	splitPause = iota
	splitSnapshot
	splitBuild
	splitLive
	splitFolders
	splitHistory
	splitDone
)

// PlaceLive is the account whose saves stay at the save path when a game
// is split on this PC: the active one, or the first of the record's.
func PlaceLive(r Record, active string) string {
	if r.Has(active) {
		return active // the saves here were played by whoever plays here
	}
	return r.Accounts[0]
}

// runSplit gives every account of the record its own folder (see Record).
func runSplit(ctx context.Context, env Env, j *Journal) error {
	r := j.Record
	if j.Built == nil {
		j.Built = map[string]bool{}
	}
	fs, err := folders(ctx, env.ST)
	if err != nil {
		return err
	}
	if j.Step == splitPause {
		live, ok := r.LivePath()
		if !ok {
			return nil // the game's folder doesn't exist on this PC: nothing to do
		}
		shared, has := fs[r.Game]
		if has {
			live = shared.Path // this PC's own path for the shared folder
		}
		_, _, anyAccount := fs.atLive(r.Game, live)
		if !has && !anyAccount && !hasAny(fs, r) {
			// This PC doesn't sync the game: account folders are adopted
			// (or not) like any folder from another PC.
			return nil
		}
		if isLink(live) {
			return errors.New("the save folder is a link to another folder; separating its saves isn't supported")
		}
		j.Live, j.Place = live, PlaceLive(r, Load().ActiveID())
		j.Built = map[string]bool{}
		if !has {
			j.Step = splitFolders // already split here; only accounts were added
		} else {
			if r.Seeder() != env.Me && time.Since(r.Created) < catchUp {
				if st, err := env.ST.FolderStatus(ctx, r.Game); err != nil || st.NeedFiles > 0 ||
					st.State == "syncing" || st.State == "sync-preparing" || st.State == "scanning" {
					return ErrWaiting
				}
			}
			if env.Busy != nil {
				if err := env.Busy(); err != nil {
					return err
				}
			}
			if open := OpenFiles(live); len(open) > 0 {
				return fmt.Errorf("a program has %s open; close the game and try again", open[0])
			}
			if err := j.pause(ctx, env.ST, r.Game); err != nil {
				_ = j.resume(ctx, env.ST, r.Game)
				_ = clearJournal()
				return err
			}
			j.Step = splitSnapshot
		}
		if err := j.save(); err != nil {
			return err
		}
	}
	undo := func(err error) error {
		// Before the save folder itself changes, a failure puts everything
		// back as it was: the shared folder syncs again, the vault copies
		// stay (they're copies; the next try moves them aside).
		_ = j.resume(ctx, env.ST, r.Game)
		_ = clearJournal()
		return err
	}
	// Resumed after an interruption: make sure Syncthing still keeps its
	// hands off the shared folder (the end of "Pause syncing" resumes
	// folders, for one).
	if j.Step > splitPause && j.Step < splitHistory {
		if _, ok := fs[r.Game]; ok {
			if err := j.pause(ctx, env.ST, r.Game); err != nil {
				return j.failAt(err)
			}
		}
	}
	if j.Step == splitSnapshot {
		if err := env.Snapshot(ctx, r.Game, r.Label, j.Live); err != nil {
			return undo(fmt.Errorf("could not save a restore point first: %w", err))
		}
		j.Step = splitBuild
		if err := j.save(); err != nil {
			return undo(err)
		}
	}
	release := func() {}
	if j.Step == splitBuild || j.Step == splitLive {
		// Released before the folders are added: adding one takes a
		// restore point, which needs the backup lock itself.
		var err error
		if release, err = env.hold(ctx); err != nil {
			if j.Step == splitBuild {
				return undo(err)
			}
			return j.failAt(err)
		}
	}
	defer func() { release() }()
	if j.Step == splitBuild {
		for _, a := range r.Accounts {
			if a == j.Place || j.Built[a] {
				continue
			}
			if err := buildVault(j.Live, a, r, r.Seeder() == env.Me); err != nil {
				return undo(err)
			}
			j.Built[a] = true
			if err := j.save(); err != nil {
				return undo(err)
			}
		}
		j.Step = splitLive
		if err := j.save(); err != nil {
			return undo(err)
		}
	}
	if j.Step == splitLive {
		if err := fixLive(env, j.Live, j.Place, r); err != nil {
			return j.failAt(err)
		}
		j.Step = splitFolders
		if err := j.save(); err != nil {
			return err
		}
	}
	release()
	release = func() {}
	if j.Step == splitFolders {
		if err := splitFolderConfig(ctx, env, j); err != nil {
			return j.failAt(err)
		}
		j.Step = splitHistory
		if err := j.save(); err != nil {
			return err
		}
	}
	if j.Step == splitHistory && env.CopyHistory != nil {
		for _, a := range r.Accounts {
			env.CopyHistory(ctx, r.Game, FolderID(r.Game, a))
		}
	}
	logx.Printf("accounts: %s is split (%s)", r.Label, strings.Join(r.Accounts, ", "))
	return nil
}

func hasAny(fs folderSet, r Record) bool {
	for _, a := range r.Accounts {
		if _, ok := fs[FolderID(r.Game, a)]; ok {
			return true
		}
	}
	return false
}

// buildVault copies the save folder into account's vault folder, with the
// conflict copies assigned to that account in place of the real files. Only
// the PC that made the decision (seed) fills the other accounts' folders: a
// PC that splits later may hold saves the others never got (it was offline
// and someone played), which must stay that player's, not become everyone's.
// Its other accounts' folders start empty and Syncthing fills them.
func buildVault(live, account string, r Record, seed bool) error {
	dst := VaultDir(live, account, r.Game)
	if exists(dst) {
		// Left over from an earlier split (or merge) of this game.
		if _, err := retire(dst); err != nil {
			return err
		}
	}
	if r.IsFresh(account) || !seed {
		return os.MkdirAll(dst, 0o755) // no saves yet (see above)
	}
	tmp := dst + ".syncer-build"
	if exists(tmp) {
		if _, err := retire(tmp); err != nil { // an unfinished copy from an interrupted try
			return err
		}
	}
	if err := copyTree(live, tmp, splitSkip); err != nil {
		_, _ = retire(tmp)
		return fmt.Errorf("could not copy the saves: %w", err)
	}
	for _, copyRel := range sortedKeys(r.Assign) {
		if r.Assign[copyRel] != account {
			continue
		}
		src := filepath.Join(live, filepath.FromSlash(copyRel))
		if !exists(src) {
			continue // resolved by hand in the meantime
		}
		orig, _, _ := conflict.Parse(filepath.Base(src))
		origRel := filepath.Join(filepath.Dir(filepath.FromSlash(copyRel)), orig)
		if err := copyVerified(src, filepath.Join(tmp, origRel)); err != nil {
			_, _ = retire(tmp)
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// fixLive settles the assigned conflict copies in the save folder itself:
// the copies that belong to the account staying here replace the real file
// (which goes to history), the others go to history (their account's vault
// already has them). Every step checks what's already done.
func fixLive(env Env, live, place string, r Record) error {
	for _, copyRel := range sortedKeys(r.Assign) {
		abs := filepath.Join(live, filepath.FromSlash(copyRel))
		if !exists(abs) {
			continue
		}
		orig, _, _ := conflict.Parse(filepath.Base(abs))
		origRel := filepath.Join(filepath.Dir(filepath.FromSlash(copyRel)), orig)
		origAbs := filepath.Join(live, origRel)
		if r.Assign[copyRel] != place {
			if err := env.Keep(r.Game, abs, origRel); err != nil {
				return fmt.Errorf("could not move %s to the history: %w", copyRel, err)
			}
			continue
		}
		if exists(origAbs) {
			if err := env.Keep(r.Game, origAbs, origRel); err != nil {
				return fmt.Errorf("could not move %s to the history: %w", origRel, err)
			}
		}
		if err := os.Rename(abs, origAbs); err != nil {
			return fmt.Errorf("could not put %s in place (is the game running?): %w", origRel, err)
		}
	}
	return nil
}

// splitFolderConfig replaces the shared folder with one folder per account.
func splitFolderConfig(ctx context.Context, env Env, j *Journal) error {
	r := j.Record
	fs, err := folders(ctx, env.ST)
	if err != nil {
		return err
	}
	if f, ok := fs[r.Game]; ok {
		if !f.Paused {
			if err := j.pause(ctx, env.ST, r.Game); err != nil {
				return err
			}
		}
		if err := env.ST.RemoveFolder(ctx, r.Game); err != nil {
			return err
		}
	}
	_, _, liveTaken := fs.atLive(r.Game, j.Live)
	for _, a := range r.Accounts {
		id := FolderID(r.Game, a)
		if _, ok := fs[id]; ok {
			continue
		}
		path := VaultDir(j.Live, a, r.Game)
		if !liveTaken && a == j.Place {
			path, liveTaken = j.Live, true
		} else if !exists(path) {
			// An account added after the split starts with no saves.
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		}
		if env.Protected != nil && len(j.Built) > 0 {
			// Copies of Live, which this split saved as a restore point
			// after it started (it built the vaults after that): saving them
			// all again takes long for a big game, longer than a try may
			// take, so it never finished.
			env.Protected(id, path, j.Started)
		}
		if err := env.AddFolder(ctx, id, FolderLabel(r.Label, Load().Name(a)), path); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---- switch --------------------------------------------------------------------

// planSwitch lists the swaps that put to's saves at every split game's save
// path on this PC.
func planSwitch(ctx context.Context, env Env, to string) ([]Move, error) {
	moves, missing, err := planMoves(ctx, env, to)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("these saves haven't reached this PC yet: %s", strings.Join(missing, ", "))
	}
	return moves, nil
}

// planMoves is planSwitch that also returns the games it can't switch yet.
func planMoves(ctx context.Context, env Env, to string) ([]Move, []string, error) {
	fs, err := folders(ctx, env.ST)
	if err != nil {
		return nil, nil, err
	}
	s := Load()
	var moves []Move
	var missing []string
	for _, r := range s.Splits() {
		live, ok := r.LivePath()
		if !ok {
			continue
		}
		out, _, ok := fs.atLive(r.Game, live)
		if !ok {
			// Maybe this PC keeps the game elsewhere; find any account folder.
			for _, a := range r.Accounts {
				if f, ok2 := fs[FolderID(r.Game, a)]; ok2 && !InVault(f.Path) {
					out, live, ok = f, f.Path, true
					break
				}
			}
		}
		if !ok {
			continue // not synced on this PC
		}
		in, has := fs[FolderID(r.Game, to)]
		if out.ID == FolderID(r.Game, to) {
			continue
		}
		if !has || !isDir(in.Path) || !exists(filepath.Join(in.Path, ".stfolder")) || isLink(live) {
			missing = append(missing, r.Label)
			continue
		}
		// Saves still on their way (or not seen at all yet, when the account
		// has some) would have the game start without them.
		if st, err := env.ST.FolderStatus(ctx, in.ID); err != nil || st.NeedFiles > 0 ||
			(r.Files > 0 && !r.IsFresh(to) && st.GlobalFiles == 0) {
			missing = append(missing, r.Label)
			continue
		}
		_, outAcct, _ := ParseFolderID(out.ID)
		moves = append(moves, Move{Game: r.Game, Live: live, Hold: live + ".syncer-switch",
			InID: in.ID, In: in.Path, OutID: out.ID, Out: VaultDir(live, outAcct, r.Game)})
	}
	return moves, missing, nil
}

// Switch makes account the active one on this PC and puts its saves of every
// split game in place.
func Switch(ctx context.Context, env Env, to string) error {
	unlock := lockOps()
	defer unlock()
	if j, ok := PendingOp(); ok {
		if err := run(ctx, env, &j); err != nil {
			return fmt.Errorf("an earlier change isn't finished: %w", err)
		}
	}
	if _, ok := Load().Get(to); !ok {
		return errors.New("unknown account")
	}
	// A split game without a folder for this account gets an empty one first.
	if env.Me != "" {
		st, err := Update(func(s *State) error {
			s.EnsureMember(to, env.Me, time.Now())
			return nil
		})
		if err != nil {
			return err
		}
		for _, r := range st.Pending() {
			j := Journal{Kind: r.Kind, Record: r, Started: time.Now()}
			err := run(ctx, env, &j)
			noteError(r.Game, err)
			if err != nil {
				return fmt.Errorf("%s: %w", r.Label, err)
			}
		}
	}
	moves, err := planSwitch(ctx, env, to)
	if err != nil {
		return err
	}
	return switchLocked(ctx, env, to, moves)
}

// switchLocked carries out planned swaps (the caller holds lockOps).
func switchLocked(ctx context.Context, env Env, to string, moves []Move) error {
	if len(moves) > 0 {
		if env.Busy != nil {
			if err := env.Busy(); err != nil {
				return err
			}
		}
		for _, m := range moves {
			if open := OpenFiles(m.Live); len(open) > 0 {
				return fmt.Errorf("a program has %s open; close the game and try again", open[0])
			}
			if exists(m.Hold) {
				return fmt.Errorf("%s is in the way; move it somewhere else first", m.Hold)
			}
		}
	}
	j := Journal{Kind: "switch", To: to, Moves: moves, Started: time.Now()}
	if err := j.save(); err != nil {
		return err
	}
	return run(ctx, env, &j)
}

func runSwitch(ctx context.Context, env Env, j *Journal) error {
	for _, m := range j.Moves {
		if m.Step > swapPause && m.Step < swapDone && env.Busy != nil {
			if err := env.Busy(); err != nil {
				return j.failAt(err) // resumed: not while a game runs either
			}
			break
		}
	}
	if len(j.Moves) > 0 {
		release, err := env.hold(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	for i := range j.Moves {
		if err := swap(ctx, env, j, &j.Moves[i]); err != nil {
			return err
		}
	}
	_, err := Update(func(s *State) error {
		s.SetActive(j.To, time.Now())
		return nil
	})
	if err == nil {
		logx.Printf("accounts: switched to %s", j.To)
	}
	return err
}

const (
	swapPause = iota
	swapHold
	swapIn
	swapOut
	swapConfig
	swapDone
)

// swap moves m.In to the save path and the saves there to m.Out. Each step
// looks at the disk before acting, so running it again after an interruption
// continues where it stopped. Until the saves at the save path have moved,
// a failure puts everything back.
func swap(ctx context.Context, env Env, j *Journal, m *Move) error {
	next := func(step int) error {
		m.Step = step
		return j.save()
	}
	back := func(err error) error {
		// Nothing has moved into the save path yet: undo this swap.
		if !exists(m.Live) && exists(m.Hold) {
			_ = os.Rename(m.Hold, m.Live)
		}
		if !exists(m.Live) || exists(m.Hold) {
			return j.failAt(err) // couldn't undo: retried from the journal
		}
		m.Step = swapPause
		first := j.Kind == "switch"
		for i := range j.Moves {
			if &j.Moves[i] != m && j.Moves[i].Step != swapPause {
				first = false // an earlier game already switched: finish the rest later
			}
		}
		if first {
			_ = j.resume(ctx, env.ST, m.InID, m.OutID)
			_ = clearJournal()
			return err
		}
		return j.failAt(err)
	}
	if m.Step > swapPause && m.Step < swapDone {
		// Resumed: both folders must still be paused while files move.
		if err := j.pause(ctx, env.ST, m.InID, m.OutID); err != nil {
			return j.failAt(err)
		}
	}
	if m.Step == swapPause {
		if err := j.pause(ctx, env.ST, m.InID, m.OutID); err != nil {
			return back(err)
		}
		if err := next(swapHold); err != nil {
			return back(err)
		}
	}
	if m.Step == swapHold {
		if !(exists(m.Hold) && !exists(m.Live)) {
			if err := os.Rename(m.Live, m.Hold); err != nil {
				return back(fmt.Errorf("could not move the current saves (is the game running?): %w", err))
			}
		}
		if err := next(swapIn); err != nil {
			return back(err)
		}
	}
	if m.Step == swapIn {
		if exists(m.Live) && exists(m.In) && exists(m.Hold) {
			// Interrupted after a cross-drive copy was put in place but
			// before the original was moved away: keep the copy if it's
			// complete, else set it aside and copy again.
			same, err := sameTree(m.In, m.Live)
			if err != nil {
				return j.failAt(err)
			}
			done := m.In
			if !same {
				done = m.Live
			}
			if _, err := retire(done); err != nil {
				return j.failAt(err)
			}
		}
		if !(exists(m.Live) && !exists(m.In)) {
			if err := moveDir(m.In, m.Live); err != nil {
				return back(fmt.Errorf("could not bring in the other saves: %w", err))
			}
		}
		if err := next(swapOut); err != nil {
			return j.failAt(err)
		}
		if j.Kind == "switch" {
			// Its saves are at a game's save folder now: whoever plays next
			// plays as this account, even if another game still has to move.
			if _, err := Update(func(s *State) error {
				s.SetActive(j.To, time.Now())
				return nil
			}); err != nil {
				return j.failAt(err)
			}
		}
	}
	if m.Step == swapOut {
		if !(exists(m.Out) && !exists(m.Hold)) {
			if exists(m.Out) {
				if _, err := retire(m.Out); err != nil {
					return j.failAt(err)
				}
			}
			if err := moveDir(m.Hold, m.Out); err != nil {
				return j.failAt(fmt.Errorf("could not put the previous saves away: %w", err))
			}
		}
		if err := next(swapConfig); err != nil {
			return j.failAt(err)
		}
	}
	if m.Step == swapConfig {
		if err := env.ST.PatchFolder(ctx, m.OutID, map[string]any{"path": m.Out}); err != nil {
			return j.failAt(err)
		}
		if err := env.ST.PatchFolder(ctx, m.InID, map[string]any{"path": m.Live}); err != nil {
			return j.failAt(err)
		}
		if j.Kind == "switch" { // a merge removes both folders next
			if err := j.resume(ctx, env.ST, m.InID, m.OutID); err != nil {
				return j.failAt(err)
			}
		}
		if err := next(swapDone); err != nil {
			return j.failAt(err)
		}
	}
	return nil
}

// ---- merge ---------------------------------------------------------------------

const (
	mergePause = iota
	mergeSnapshot
	mergeSwap
	mergeFolders
	mergeHistory
	mergeDone
)

// runMerge makes a split game one shared folder again, with the winner's
// saves. Every account's saves are kept as a pinned restore point first, and
// the other accounts' folders stay in the vault.
func runMerge(ctx context.Context, env Env, j *Journal) error {
	r := j.Record
	fs, err := folders(ctx, env.ST)
	if err != nil {
		return err
	}
	var ids []string
	for _, a := range r.Accounts {
		if _, ok := fs[FolderID(r.Game, a)]; ok {
			ids = append(ids, FolderID(r.Game, a))
		}
	}
	if j.Step == mergePause {
		if len(ids) == 0 {
			return nil // not synced here: the shared folder is adopted like any other
		}
		live, found := r.LivePath()
		if found {
			_, _, found = fs.atLive(r.Game, live)
		}
		if !found {
			for _, id := range ids {
				if !InVault(fs[id].Path) {
					live, found = fs[id].Path, true
					break
				}
			}
		}
		if !found {
			return errors.New("none of the saves are at the game's save folder on this PC")
		}
		if isLink(live) {
			return errors.New("the save folder is a link to another folder; this isn't supported")
		}
		win := FolderID(r.Game, r.Winner)
		wf, haveWin := fs[win]
		haveWin = haveWin && isDir(wf.Path) && exists(filepath.Join(wf.Path, ".stfolder"))
		ready := haveWin
		if haveWin && r.By != env.Me {
			st, err := env.ST.FolderStatus(ctx, win)
			ready = err == nil && st.NeedFiles == 0 && st.State != "syncing" && st.State != "sync-preparing" && st.State != "scanning"
		}
		switch {
		case ready:
		case r.By == env.Me:
			return errors.New("those saves aren't on this PC; pick them on a PC that has them, or wait until they arrive")
		case time.Since(r.Created) < catchUp:
			return ErrWaiting
		default:
			// They never arrived here: share the game with an empty folder
			// that the other PCs fill with the winner's saves.
			j.EmptyLive = true
		}
		if env.Busy != nil {
			if err := env.Busy(); err != nil {
				return err
			}
		}
		if open := OpenFiles(live); len(open) > 0 {
			return fmt.Errorf("a program has %s open; close the game and try again", open[0])
		}
		j.Live = live
		if err := j.save(); err != nil {
			return err
		}
		if err := j.pause(ctx, env.ST, ids...); err != nil {
			_ = j.resume(ctx, env.ST, ids...)
			_ = clearJournal()
			return err
		}
		j.Step = mergeSnapshot
		if err := j.save(); err != nil {
			return err
		}
	}
	if j.Step > mergePause && j.Step < mergeHistory {
		if err := j.pause(ctx, env.ST, ids...); err != nil {
			return j.failAt(err)
		}
	}
	if j.Step == mergeSnapshot {
		for _, id := range ids {
			if err := env.Snapshot(ctx, id, r.Label, fs[id].Path); err != nil {
				_ = j.resume(ctx, env.ST, ids...)
				_ = clearJournal()
				return fmt.Errorf("could not save a restore point first: %w", err)
			}
		}
		j.Step = mergeSwap
		win := FolderID(r.Game, r.Winner)
		if out, _, ok := fs.atLive(r.Game, j.Live); ok && out.ID != win && !j.EmptyLive {
			if in, ok := fs[win]; ok && isDir(in.Path) {
				_, outAcct, _ := ParseFolderID(out.ID)
				j.Moves = []Move{{Game: r.Game, Live: j.Live, Hold: j.Live + ".syncer-switch",
					InID: win, In: in.Path, OutID: out.ID, Out: VaultDir(j.Live, outAcct, r.Game)}}
			}
		}
		if err := j.save(); err != nil {
			return err
		}
	}
	if j.Step == mergeSwap {
		release := func() {}
		if len(j.Moves) > 0 {
			var err error
			if release, err = env.hold(ctx); err != nil {
				return j.failAt(err)
			}
		}
		for i := range j.Moves {
			if err := swap(ctx, env, j, &j.Moves[i]); err != nil {
				release()
				return err // swap recorded where it stopped (and why)
			}
		}
		release() // adding the shared folder takes a restore point, which needs the lock
		j.Step = mergeFolders
		if err := j.save(); err != nil {
			return err
		}
	}
	if j.Step == mergeFolders {
		fs, err := folders(ctx, env.ST)
		if err != nil {
			return err
		}
		if _, ok := fs[r.Game]; !ok {
			for _, a := range r.Accounts {
				id := FolderID(r.Game, a)
				if f, ok := fs[id]; ok {
					if !f.Paused {
						if err := j.pause(ctx, env.ST, id); err != nil {
							return j.failAt(err)
						}
					}
					if err := env.ST.RemoveFolder(ctx, id); err != nil {
						return j.failAt(err)
					}
				}
			}
			if j.EmptyLive && exists(j.Live) {
				// Saved as a restore point above; set aside, not deleted.
				if _, err := retire(j.Live); err != nil {
					return j.failAt(err)
				}
			}
			if err := os.MkdirAll(j.Live, 0o755); err != nil {
				return j.failAt(err)
			}
			if err := env.AddFolder(ctx, r.Game, r.Label, j.Live); err != nil {
				return j.failAt(err)
			}
		}
		// The other accounts' saves (kept as restore points above) leave the
		// vault for its .trash, so a later split starts clean.
		for _, a := range r.Accounts {
			if v := VaultDir(j.Live, a, r.Game); exists(v) && !samePath(v, j.Live) {
				if _, err := retire(v); err != nil {
					logx.Printf("accounts: %v", err)
				}
			}
		}
		j.Step = mergeHistory
		if err := j.save(); err != nil {
			return err
		}
	}
	if j.Step == mergeHistory && env.CopyHistory != nil {
		env.CopyHistory(ctx, FolderID(r.Game, r.Winner), r.Game)
	}
	logx.Printf("accounts: %s is shared again (%s's saves)", r.Label, r.Winner)
	return nil
}

// ---- starting operations here --------------------------------------------------

// Start records a decision made on this PC and carries it out right away.
// Other PCs pick the record up from the shared metadata and do the same.
func Start(ctx context.Context, env Env, rec Record) error {
	unlock := lockOps()
	defer unlock()
	if j, ok := PendingOp(); ok {
		if err := run(ctx, env, &j); err != nil {
			return fmt.Errorf("an earlier change isn't finished: %w", err)
		}
	}
	if !ValidRecord(rec) {
		return errors.New("that can't be done with this game")
	}
	// Check the game is closed before the decision is shared with the other
	// PCs (they'd carry it out even if this PC couldn't).
	if env.Busy != nil {
		if err := env.Busy(); err != nil {
			return err
		}
	}
	if fs, err := folders(ctx, env.ST); err == nil {
		for id, f := range fs {
			if g, _, ok := ParseFolderID(id); (ok && g == rec.Game) || id == rec.Game {
				if open := OpenFiles(f.Path); len(open) > 0 {
					return fmt.Errorf("a program has %s open; close the game and try again", open[0])
				}
			}
		}
	}
	if _, err := Update(func(s *State) error {
		if cur, ok := s.Record(rec.Game); ok && !Later(rec, cur) {
			return errors.New("another PC changed this game in the meantime; try again")
		}
		s.Put(rec)
		return nil
	}); err != nil {
		return err
	}
	j := Journal{Kind: rec.Kind, Record: rec, Started: time.Now()}
	return run(ctx, env, &j)
}

// ValidLabel is a record's label trimmed for display.
func ValidLabel(label, id string) string {
	if l := strings.TrimSpace(label); l != "" {
		if len(l) > 200 {
			l = l[:200]
		}
		return l
	}
	return id
}

// isLink reports whether p is a symbolic link or junction: its files live
// somewhere else, and moving or copying it wouldn't move them.
func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

// OpFolders lists the Syncthing folders an unfinished operation works on:
// they must stay paused until it's done (see Journal.pause).
func OpFolders() []string {
	j, ok := PendingOp()
	if !ok {
		return nil
	}
	ids := []string{j.Record.Game}
	for _, a := range j.Record.Accounts {
		ids = append(ids, FolderID(j.Record.Game, a))
	}
	for _, m := range j.Moves {
		ids = append(ids, m.InID, m.OutID)
	}
	return ids
}

// Locked runs fn while no account operation can start, e.g. to add a
// folder at a save folder that a split might be moving right now.
func Locked(fn func() error) error {
	unlock := lockOps()
	defer unlock()
	if _, busy := PendingOp(); busy {
		return ErrBusy
	}
	return fn()
}

// ErrBusy means an account operation hasn't finished yet.
var ErrBusy = errors.New("saves are being moved between accounts; try again shortly")
