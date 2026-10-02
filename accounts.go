package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

func init() {
	meta.ApplyAccounts = func(ctx context.Context, c *syncthing.Client, me string, others []string) error {
		_, err := accounts.Apply(ctx, accountsEnv(c, me, others))
		// Another PC split a game this PC syncs: show accounts here too, or
		// nobody could see or switch whose saves are in use.
		if st := accounts.Load(); len(st.Splits()) > 0 && !store.LoadSettings().Accounts {
			if _, e := store.UpdateSettings(func(s *store.Settings) { s.Accounts = true }); e == nil {
				logx.Printf("accounts: turned on (a game has separate saves per account)")
			}
		}
		return err
	}
}

// historyTarget is where replaced saves go: the backup, or local snapshots.
func historyTarget() string {
	if t, ok := backupTarget(store.LoadSettings()); ok {
		if err := os.MkdirAll(t, 0o755); err == nil {
			return t
		}
	}
	return snapshotDir()
}

// accountsEnv connects the account operations to Syncthing and the backup.
func accountsEnv(c *syncthing.Client, me string, others []string) accounts.Env {
	return accounts.Env{
		ST: c,
		Me: me,
		AddFolder: func(ctx context.Context, id, label, path string) error {
			return meta.AddFolder(ctx, c, id, label, path, me, others)
		},
		Snapshot: func(ctx context.Context, id, label, path string) error {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
			defer cancel()
			n, err := backup.Snapshot(ctx, historyTarget(), backup.Folder{ID: id, Label: label, Path: path})
			if err == nil && n > 0 {
				logx.Printf("accounts: saved %d file(s) of %s as a restore point", n, label)
			}
			return err
		},
		Keep: func(id, abs, rel string) error {
			// Called while the operation holds the backup lock.
			_, err := backup.KeepLocked(historyTarget(), id, abs, rel, true)
			return err
		},
		// A split's own restore point covers the account folders made from
		// the game's folder.
		Protected: markProtected,
		CopyHistory: func(_ context.Context, from, to string) {
			// The game's whole backup and history: for a big game that takes
			// long, so it happens after the split (see copyHistories).
			store.UpdateState(func(st *store.State) {
				if st.HistoryCopies == nil {
					st.HistoryCopies = map[string]string{}
				}
				st.HistoryCopies[to] = from
			})
			go copyHistories(context.Background())
		},
		Busy: noGameRunning,
		NoSplit: func(game string) error {
			if l, ok := store.LoadSettings().Launchers[game]; ok {
				return fmt.Errorf("this is %s's own data; it keeps each account's apart by itself", l)
			}
			return nil
		},
		HoldBackups: func(ctx context.Context) (func(), error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			for {
				if unlock, err := backup.Lock(); err == nil {
					return unlock, nil
				}
				select {
				case <-ctx.Done():
					return nil, errors.New("a backup is running; try again when it's done")
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
}

var historyMu sync.Mutex

// copyHistories gives the account folders of a split the backup history of
// the game they were split from (store.State.HistoryCopies). One that
// doesn't finish (a backup is running, Syncer exits) is finished by a later
// call: files already copied are skipped.
func copyHistories(ctx context.Context) {
	if !historyMu.TryLock() {
		return // already at it; it picks up what was just added
	}
	defer historyMu.Unlock()
	for {
		pending := store.LoadState().HistoryCopies
		if len(pending) == 0 || ctx.Err() != nil {
			return
		}
		t, ok := backupTarget(store.LoadSettings())
		if !ok {
			return // no backup to copy from: tried again once there is
		}
		progress := false
		for to, from := range pending {
			if err := backup.CopyHistory(ctx, t, from, to); err != nil {
				logx.Printf("accounts: copy history %s → %s: %v", from, to, err)
				continue
			}
			store.UpdateState(func(st *store.State) { delete(st.HistoryCopies, to) })
			progress = true
		}
		if !progress {
			return
		}
	}
}

// noGameRunning refuses while any installed game runs: its saves are about to
// move, and a game writing into a folder that moved away loses that save.
func noGameRunning() error {
	if exe, dir := cachedInstalled().RunningGame(winx.ProcessPaths()); exe != "" {
		return fmt.Errorf("%s is running (%s); close it first (its saves are about to move)", filepath.Base(dir), filepath.Base(exe))
	}
	return nil
}

// withEnv runs fn with the account operations' environment.
func (a *App) withEnv(fn func(ctx context.Context, c *syncthing.Client, env accounts.Env) error) error {
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
	ds, err := c.Devices(ctx)
	if err != nil {
		return err
	}
	var others []string
	for _, d := range ds {
		if d.DeviceID != st.MyID {
			others = append(others, d.DeviceID)
		}
	}
	err = fn(ctx, c, accountsEnv(c, st.MyID, others))
	_, _ = meta.Reconcile(ctx, c) // share the change with the other PCs
	_ = syncPause(ctx, c)         // new folders pause too while syncing is paused
	meta.RefreshCache(ctx, c)     // backups while Syncthing is down use the new paths
	a.forgetConflicts()
	a.forgetDetails()
	a.emitChanged()
	a.refreshTray()
	return err
}

// emitChanged tells the window to refresh (the --api helper has none).
func (a *App) emitChanged() {
	if !a.headless && a.ctx != nil {
		runtime.EventsEmit(a.ctx, "changed")
	}
}

// ---- views -------------------------------------------------------------------

type AccountView struct {
	accounts.Account
	Active bool        `json:"active"`
	Games  []SplitSave `json:"games"` // split games: this account's own saves
	PCs    []string    `json:"pcs"`   // PCs this account is playing on right now
}

// SplitSave is one account's saves of a split game.
type SplitSave struct {
	Game      string     `json:"game"`     // the game's shared id
	FolderID  string     `json:"folderID"` // this account's folder
	Label     string     `json:"label"`
	Path      string     `json:"path"`
	Here      bool       `json:"here"`   // at the game's save folder (the active account's)
	Synced    bool       `json:"synced"` // this PC syncs it
	Files     []SaveFile `json:"files"`
	More      int        `json:"more"` // files not listed
	Bytes     int64      `json:"bytes"`
	Conflicts int        `json:"conflicts"`
	Modified  time.Time  `json:"modified"`
	// Launcher: a launcher's own data (its name), which the launcher keeps
	// per account by itself; not a split game.
	Launcher string `json:"launcher,omitempty"`
}

type SaveFile struct {
	Rel      string    `json:"rel"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Owner    string    `json:"owner,omitempty"` // account id that probably wrote it (shared games)
}

type AccountsView struct {
	Enabled  bool          `json:"enabled"`
	Active   string        `json:"active"`
	Accounts []AccountView `json:"accounts"`
	Splits   []SplitView   `json:"splits"`
	// Waiting lists paired PCs whose Syncer can't split games yet.
	Waiting []string `json:"waiting"`
	// Op is an account change that didn't finish (it's retried).
	Op      string `json:"op"`
	OpLabel string `json:"opLabel"` // the game, or the account switched to
	OpError string `json:"opError"`
	// Pending: splits and merges (usually from another PC) not carried out
	// on this PC yet, and why.
	Pending []PendingChange `json:"pending"`
}

type PendingChange struct {
	Game  string `json:"game"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Error string `json:"error"`
	Mine  bool   `json:"mine"` // decided on this PC
}

type SplitView struct {
	Game     string   `json:"game"`
	Label    string   `json:"label"`
	Accounts []string `json:"accounts"`
	Here     string   `json:"here"` // whose saves are at the save folder on this PC
}

// Accounts describes the accounts and whose saves are where.
func (a *App) Accounts() (AccountsView, error) {
	st := accounts.Load()
	st.Clean()
	v := AccountsView{Enabled: store.LoadSettings().Accounts || len(st.Splits()) > 0, Active: st.ActiveID(),
		Pending: []PendingChange{}}
	opGame := ""
	if j, ok := accounts.PendingOp(); ok {
		v.Op, v.OpLabel, v.OpError = j.Kind, j.Record.Label, j.Error
		if j.Kind == "switch" {
			v.OpLabel = st.Name(j.To)
		} else {
			opGame = j.Record.Game
		}
	}
	folders := map[string]syncthing.Folder{}
	me := ""
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		if fs, err := c.Folders(ctx); err == nil {
			for _, f := range fs {
				folders[f.ID] = f
			}
		}
		if s, err := c.Status(ctx); err == nil {
			me = s.MyID
		}
		if ds, err := c.Devices(ctx); err == nil {
			var ids []string
			names := map[string]string{}
			for _, d := range ds {
				if d.DeviceID != me {
					ids = append(ids, d.DeviceID)
					names[d.DeviceID] = cmpOr(d.Name, d.DeviceID[:7])
				}
			}
			for _, id := range meta.WithoutFeature(ids, accounts.Feature) {
				v.Waiting = append(v.Waiting, names[id])
			}
		}
		cancel()
	}
	for _, r := range st.Pending() {
		if r.Game == opGame {
			continue // the unfinished change above is this one
		}
		v.Pending = append(v.Pending, PendingChange{Game: r.Game, Label: r.Label, Kind: r.Kind, Error: st.Errors[r.Game], Mine: me != "" && r.By == me})
	}
	playing := map[string][]string{} // account -> PCs
	if a := st.ActiveID(); a != "" {
		playing[a] = append(playing[a], hostname()+" (this PC)")
	}
	if me != "" {
		peerNames := meta.Peers()
		for dev, sh := range meta.PeerAccounts(me) {
			if _, ok := st.Get(sh.Active); ok {
				playing[sh.Active] = append(playing[sh.Active], cmpOr(peerNames[dev], dev[:7]))
			}
		}
	}
	launchers := store.LoadSettings().Launchers
	launcherIDs := make([]string, 0, len(launchers))
	for id := range launchers {
		launcherIDs = append(launcherIDs, id)
	}
	sort.Strings(launcherIDs)
	for _, acc := range st.Live() {
		av := AccountView{Account: acc, Active: acc.ID == v.Active, PCs: playing[acc.ID], Games: []SplitSave{}}
		for _, r := range st.Splits() {
			if !r.Has(acc.ID) {
				continue
			}
			av.Games = append(av.Games, splitSave(r, acc.ID, folders))
		}
		for _, id := range launcherIDs {
			if f, ok := folders[id]; ok {
				if s, ok := launcherSave(f, launchers[id], acc.ID, av.Active); ok {
					av.Games = append(av.Games, s)
				}
			}
		}
		v.Accounts = append(v.Accounts, av)
	}
	for _, r := range st.Splits() {
		sv := SplitView{Game: r.Game, Label: r.Label, Accounts: r.Accounts}
		if live, ok := r.LivePath(); ok {
			for id, f := range folders {
				if g, acc, ok := accounts.ParseFolderID(id); ok && g == r.Game && samePath(f.Path, live) {
					sv.Here = acc
				}
			}
		}
		v.Splits = append(v.Splits, sv)
	}
	return v, nil
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

const maxListed = 200

// splitSave lists an account's saves of a split game on this PC.
func splitSave(r accounts.Record, acc string, folders map[string]syncthing.Folder) SplitSave {
	id := accounts.FolderID(r.Game, acc)
	s := SplitSave{Game: r.Game, FolderID: id, Label: r.Label, Files: []SaveFile{}}
	f, ok := folders[id]
	if !ok {
		return s
	}
	s.Synced, s.Path = true, f.Path
	if live, ok := r.LivePath(); ok {
		s.Here = samePath(live, f.Path)
	}
	s.Files, s.More, s.Bytes, s.Modified = listSaves(f.Path)
	s.Conflicts = conflict.Count(f.Path)
	return s
}

// launcherSave lists an account's part of a launcher's own data on this
// PC: the launcher keeps it in a subfolder named after the account (see
// launcherdata.go). False when the account has none here.
func launcherSave(f syncthing.Folder, launcher, acc string, active bool) (SplitSave, bool) {
	dir := filepath.Join(f.Path, acc)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return SplitSave{}, false
	}
	s := SplitSave{Game: f.ID, FolderID: f.ID, Label: cmpOr(f.Label, f.ID), Path: dir, Here: active, Synced: true, Launcher: launcher}
	s.Files, s.More, s.Bytes, s.Modified = listSaves(dir)
	s.Conflicts = conflict.Count(dir)
	return s, true
}

// listSaves lists the save files in dir (Syncthing's own files left out),
// newest first.
func listSaves(dir string) (files []SaveFile, more int, bytes int64, newest time.Time) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if n := strings.ToLower(d.Name()); rel != "." && (n == ".stfolder" || n == ".stversions") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(d.Name(), ".stignore") || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		bytes += fi.Size()
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		files = append(files, SaveFile{Rel: filepath.ToSlash(rel), Size: fi.Size(), Modified: fi.ModTime()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Modified.After(files[j].Modified) })
	if len(files) > maxListed {
		more = len(files) - maxListed
		files = files[:maxListed]
	}
	if files == nil {
		files = []SaveFile{}
	}
	return files, more, bytes, newest
}

// ---- owners --------------------------------------------------------------------

// owners guesses who wrote files: the PC that last changed a file (from
// Syncthing) and who was playing on that PC at that time.
type owners struct {
	logs map[string][]accounts.ActiveEntry // short device id -> active log
}

func (a *App) ownerGuesser(me string) owners {
	o := owners{logs: map[string][]accounts.ActiveEntry{}}
	for dev, sh := range meta.PeerAccounts(me) {
		if len(dev) >= 7 {
			o.logs[dev[:7]] = sh.ValidLog()
		}
	}
	if len(me) >= 7 {
		o.logs[me[:7]] = accounts.Load().ValidLog()
	}
	return o
}

func (o owners) at(device string, t time.Time) string {
	return accounts.ActiveAt(o.logs[device], t)
}

// ConflictOwner is who probably made each version of a conflict.
type ConflictOwner struct {
	Copy    string `json:"copy"`    // the conflict copy's path
	Current string `json:"current"` // account id that probably wrote the current file ("" = unknown)
	Other   string `json:"other"`   // … and the conflict copy
}

// ConflictOwners guesses whose each version of a folder's conflicts is.
func (a *App) ConflictOwners(id string) ([]ConflictOwner, error) {
	f, err := folderByID(id)
	if err != nil {
		return nil, err
	}
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	o := a.ownerGuesser(st.MyID)
	out := []ConflictOwner{}
	for _, cf := range conflict.Find(f.Path) {
		co := ConflictOwner{Copy: cf.Copy, Other: o.at(cf.Device, cf.CopyModified)}
		if fi, err := c.DBFile(ctx, id, filepath.ToSlash(cf.Rel)); err == nil && fi.ModifiedBy != "" {
			co.Current = o.at(fi.ModifiedBy, fi.Modified)
		}
		out = append(out, co)
	}
	return out, nil
}

// SaveOwners lists a shared game's files with who probably wrote each one
// (e.g. whose save slot it is).
func (a *App) SaveOwners(id string) ([]SaveFile, error) {
	f, err := folderByID(id)
	if err != nil {
		return nil, err
	}
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	o := a.ownerGuesser(st.MyID)
	files, _, _, _ := listSaves(f.Path)
	if len(files) > 50 {
		files = files[:50] // one Syncthing call per file
	}
	for i := range files {
		if fi, err := c.DBFile(ctx, id, files[i].Rel); err == nil && fi.ModifiedBy != "" {
			files[i].Owner = o.at(fi.ModifiedBy, fi.Modified)
		}
	}
	return files, nil
}

// ---- managing accounts -----------------------------------------------------------

// SetAccountsEnabled turns the accounts feature on or off on this PC. It
// can't be turned off while games are split: their saves would have nowhere
// to go.
func (a *App) SetAccountsEnabled(on bool) error {
	if !on {
		if sp := accounts.Load().Splits(); len(sp) > 0 {
			var names []string
			for _, r := range sp {
				names = append(names, r.Label)
			}
			return fmt.Errorf("these games have separate saves per account; merge them first: %s", strings.Join(names, ", "))
		}
	}
	_, err := store.UpdateSettings(func(s *store.Settings) { s.Accounts = on })
	a.emitChanged()
	a.refreshTray()
	return err
}

// CreateAccount adds a person. The first account becomes this PC's active
// one. Games already split get an (empty) folder for the new account, so
// they start fresh instead of playing on someone else's saves.
func (a *App) CreateAccount(name, color string) (accounts.Account, error) {
	name, err := accounts.CleanName(name)
	if err != nil {
		return accounts.Account{}, err
	}
	if !accounts.ValidColor(color) {
		return accounts.Account{}, errors.New("invalid color")
	}
	var acc accounts.Account
	me := deviceID() // (a Syncthing call: not while holding the accounts lock)
	_, err = accounts.Update(func(s *accounts.State) error {
		if len(s.Live()) >= accounts.MaxAccounts {
			return errors.New("that's the most accounts Syncer supports")
		}
		for _, x := range s.Live() {
			if strings.EqualFold(x.Name, name) {
				return errors.New("there's already an account with that name")
			}
		}
		now := time.Now()
		acc = accounts.Account{ID: accounts.NewAccountID(func(id string) bool {
			for _, x := range s.Accounts {
				if x.ID == id {
					return true
				}
			}
			return false
		}), Name: name, Color: color, Created: now, Updated: now}
		s.Accounts = append(s.Accounts, acc)
		if len(s.Live()) == 1 {
			s.SetActive(acc.ID, now)
		}
		s.EnsureMember(acc.ID, me, now) // an empty folder in every split game
		return nil
	})
	if err != nil {
		return acc, err
	}
	logx.Printf("accounts: added %s", name)
	err = a.withEnv(func(ctx context.Context, _ *syncthing.Client, env accounts.Env) error {
		_, err := accounts.Apply(ctx, env)
		return err
	})
	if err != nil {
		logx.Printf("accounts: %v", err) // retried on the next reconcile
	}
	return acc, nil
}

// deviceID is this PC's Syncthing id ("" when Syncthing isn't reachable).
func deviceID() string {
	c, err := syncthing.New()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return ""
	}
	return st.MyID
}

// EditAccount renames an account or changes its color, on every PC.
func (a *App) EditAccount(id, name, color string) error {
	name, err := accounts.CleanName(name)
	if err != nil {
		return err
	}
	if !accounts.ValidColor(color) {
		return errors.New("invalid color")
	}
	_, err = accounts.Update(func(s *accounts.State) error {
		for _, x := range s.Live() {
			if x.ID != id && strings.EqualFold(x.Name, name) {
				return errors.New("there's already an account with that name")
			}
		}
		for i, x := range s.Accounts {
			if x.ID == id && !x.Deleted {
				s.Accounts[i].Name, s.Accounts[i].Color, s.Accounts[i].Updated = name, color, time.Now()
				return nil
			}
		}
		return errors.New("unknown account")
	})
	if err == nil {
		a.shareAccounts()
	}
	return err
}

// DeleteAccount removes an account that owns no split game's saves.
func (a *App) DeleteAccount(id string) error {
	_, err := accounts.Update(func(s *accounts.State) error {
		if s.InUse(id) {
			return accounts.ErrInUse
		}
		for i, x := range s.Accounts {
			if x.ID == id && !x.Deleted {
				s.Accounts[i].Deleted, s.Accounts[i].Updated = true, time.Now()
				if s.Active == id {
					s.SetActive(s.ActiveID(), time.Now())
				}
				return nil
			}
		}
		return errors.New("unknown account")
	})
	if err == nil {
		a.shareAccounts()
	}
	return err
}

// shareAccounts publishes account changes right away.
func (a *App) shareAccounts() {
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		_, _ = meta.Reconcile(ctx, c)
		cancel()
	}
	a.emitChanged()
	a.refreshTray()
}

// SwitchAccount makes id the account playing on this PC: its saves of every
// split game are put in place (the previous account's are kept in the vault).
func (a *App) SwitchAccount(id string) error {
	st := accounts.Load()
	if _, ok := st.Get(id); !ok {
		return errors.New("unknown account")
	}
	// No game has separate saves: nothing to move, Syncthing not needed.
	errSplit := errors.New("split")
	if _, err := accounts.Update(func(s *accounts.State) error {
		if len(s.Splits()) > 0 {
			return errSplit
		}
		s.SetActive(id, time.Now())
		return nil
	}); err == nil {
		a.shareAccounts()
		return nil
	} else if err != errSplit {
		return err
	}
	return a.withEnv(func(ctx context.Context, _ *syncthing.Client, env accounts.Env) error {
		return accounts.Switch(ctx, env, id)
	})
}

// RetryAccountChange finishes an account change that stopped halfway.
func (a *App) RetryAccountChange() error {
	return a.withEnv(func(ctx context.Context, _ *syncthing.Client, env accounts.Env) error {
		_, err := accounts.Apply(ctx, env)
		return err
	})
}

// SplitGame gives every account its own saves of a game. assign gives
// conflict copies (by path, as Conflicts lists them) to accounts: that
// account gets the copy's version of the file, the others the current one.
func (a *App) SplitGame(id string, assign map[string]string) error {
	st := accounts.Load()
	if !store.LoadSettings().Accounts {
		return errors.New("turn on accounts in Settings first")
	}
	live := st.Live()
	if len(live) < 2 {
		return errors.New("add a second account first")
	}
	if _, _, ok := accounts.ParseFolderID(id); ok {
		return errors.New("this game already has separate saves per account")
	}
	if l, ok := store.LoadSettings().Launchers[id]; ok {
		return fmt.Errorf("this is %s's own data; it keeps each account's apart by itself", l)
	}
	return a.withEnv(func(ctx context.Context, c *syncthing.Client, env accounts.Env) error {
		f, err := findFolder(ctx, c, id)
		if err != nil {
			return err
		}
		ds, err := c.Devices(ctx)
		if err != nil {
			return err
		}
		me := deviceID()
		var ids []string
		for _, d := range ds {
			if d.DeviceID != me {
				ids = append(ids, d.DeviceID)
			}
		}
		if w := meta.WithoutFeature(ids, accounts.Feature); len(w) > 0 {
			var names []string
			for _, d := range ds {
				for _, x := range w {
					if d.DeviceID == x {
						names = append(names, cmpOr(d.Name, x[:7]))
					}
				}
			}
			return fmt.Errorf("update Syncer on %s first and let it connect once (a device without Syncer, like a NAS, has to be removed under Devices first)", strings.Join(names, ", "))
		}
		root, rel, ok := portable(f.Path)
		if !ok {
			return errors.New("this game's save folder can't be split between accounts")
		}
		r := accounts.Record{Kind: accounts.KindSplit, Game: f.ID, Label: cmpOr(f.Label, f.ID), Root: root, Rel: rel,
			Gen: st.NextGen(f.ID), Created: time.Now(), By: me, Origin: me, Assign: map[string]string{}}
		for _, acc := range live {
			r.Accounts = append(r.Accounts, acc.ID)
			if !validFolderID(accounts.FolderID(f.ID, acc.ID)) {
				return errors.New("this game's folder id is too long to split")
			}
		}
		files, _, _, _ := listSaves(f.Path)
		r.Files = len(files)
		for _, sf := range files {
			if _, _, ok := conflict.Parse(filepath.Base(sf.Rel)); ok {
				r.Files-- // conflict copies aren't carried over
			}
		}
		copies := map[string]bool{}
		for _, cf := range conflict.Find(f.Path) {
			copies[filepath.ToSlash(cf.Copy)] = true
		}
		for copyRel, acc := range assign {
			copyRel = filepath.ToSlash(copyRel)
			if !copies[copyRel] {
				return fmt.Errorf("%s is no longer there; reopen the list", copyRel)
			}
			if _, ok := st.Get(acc); !ok {
				return errors.New("unknown account")
			}
			r.Assign[copyRel] = acc
		}
		if err := accounts.Start(ctx, env, r); err != nil {
			return err
		}
		logx.Printf("accounts: split %s between %d accounts", r.Label, len(r.Accounts))
		return nil
	})
}

// MergeGame makes a split game shared again with winner's saves. Every
// account's saves are kept as a restore point first.
func (a *App) MergeGame(game, winner string) error {
	st := accounts.Load()
	r, ok := st.Split(game)
	if !ok {
		return errors.New("this game isn't split")
	}
	if !r.Has(winner) {
		return errors.New("that account has no saves of this game")
	}
	return a.withEnv(func(ctx context.Context, _ *syncthing.Client, env accounts.Env) error {
		m := r
		m.Kind, m.Winner, m.Assign, m.Gen, m.Created, m.By = accounts.KindMerge, winner, nil, r.Gen+1, time.Now(), deviceID()
		if err := accounts.Start(ctx, env, m); err != nil {
			return err
		}
		logx.Printf("accounts: %s is shared again, with %s's saves", r.Label, st.Name(winner))
		return nil
	})
}

func portable(p string) (string, string, bool) {
	root, rel, ok := paths.Portable(p)
	return root, rel, ok && paths.CheckSyncable(p) == nil
}

func validFolderID(id string) bool { return paths.ValidID(id) }

// splitGuard refuses changes that don't fit a game split per account.
func splitGuard(id string) error {
	if _, _, ok := accounts.ParseFolderID(id); ok {
		return errors.New("this game has separate saves per account; merge it first (Accounts)")
	}
	return nil
}

// inVault reports whether folder id is another account's saves in the vault.
func inVault(fs []syncthing.Folder, id string) bool {
	for _, f := range fs {
		if f.ID == id {
			return accounts.InVault(f.Path)
		}
	}
	return false
}
