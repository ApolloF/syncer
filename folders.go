package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// ---- folders -----------------------------------------------------------------

type FolderView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Path      string    `json:"path"`
	State     string    `json:"state"`
	Bytes     int64     `json:"bytes"`
	Files     int       `json:"files"`
	NeedBytes int64     `json:"needBytes"`
	Errors    int       `json:"errors"`
	Problem   string    `json:"problem"` // what's wrong, in plain words ("" when nothing is)
	Backup    bool      `json:"backup"`
	Sync      bool      `json:"sync"`      // false = backed up here but not synced
	Installed bool      `json:"installed"` // only meaningful with "only installed games" on
	Exists    bool      `json:"exists"`
	Shared    int       `json:"shared"`
	Conflicts int       `json:"conflicts"` // two versions of a save exist
	Modified  time.Time `json:"modified"`

	BackedUp    time.Time `json:"backedUp"`    // last backup without errors on this PC
	BackupBytes int64     `json:"backupBytes"` // size of its backup
	Points      int       `json:"points"`      // restore points
	Exclude     []string  `json:"exclude"`     // file patterns skipped on this PC
	NewerOn     string    `json:"newerOn"`     // another PC backed up a newer save that isn't here yet
	NewerAt     time.Time `json:"newerAt"`
	NewerCanGet bool      `json:"newerCanGet"` // … and it can be taken from that PC's backup ("Get it")
	NewerWhy    string    `json:"newerWhy"`    // why it wasn't taken by itself

	// Repairable: a launcher's data folder emptied or replaced outside
	// Syncer, which RepairFolder can fix now (see launcherrepair.go).
	Repairable bool `json:"repairable"`

	Inside   string `json:"inside"`   // id of another synced folder that holds this one (synced twice)
	OneDrive bool   `json:"oneDrive"` // OneDrive syncs this folder too

	SteamCloud        bool   `json:"steamCloud"`        // Steam Cloud keeps this folder on this PC
	UbisoftCloud      bool   `json:"ubisoftCloud"`      // Ubisoft Connect's own save folder, in its cloud
	CopyOf            string `json:"copyOf"`            // an emulator's copy of this game's own saves
	OneDriveCopy      string `json:"oneDriveCopy"`      // another copy on the other side of OneDrive
	OneDriveCopyNewer bool   `json:"oneDriveCopyNewer"` // … with newer saves than this one

	// Mod folders: their kind (mods.Kind*), the mod manager's game id, and
	// for deployed mods this PC's role and update state.
	Kind       string `json:"kind"`
	ModGame    string `json:"modGame"`
	ModRole    string `json:"modRole"`
	ModPhase   string `json:"modPhase"`   // idle, pending, applying, held
	ModHeld    string `json:"modHeld"`    // why updates are held
	ModHeldBy  string `json:"modHeldBy"`  // what held them (see held* in deployed.go)
	ModPending string `json:"modPending"` // what the next update changes
	// A game split per account: these are Account's saves (the account
	// playing on this PC); Game is the game's shared id.
	Split   bool   `json:"split"`
	Account string `json:"account"`
	Game    string `json:"game"`
}

// Folders lists synced folders plus this PC's backup-only ones. Backup-only
// games still show when Syncthing is down (e.g. after "Undo everything").
func (a *App) Folders() ([]FolderView, error) {
	s := store.LoadSettings()
	installed := func(string) bool { return true }
	if s.InstalledOnly {
		installed = cachedInstalled().Has
	}
	var out []FolderView
	c, err := a.client()
	if err == nil {
		ctx, cancel := a.callCtx()
		defer cancel()
		var fs []syncthing.Folder
		if fs, err = c.Folders(ctx); err == nil {
			var bf, saves []backup.Folder
			for _, f := range fs {
				if f.ID != meta.FolderID {
					bf = append(bf, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
					if !isMod(s, f.ID) {
						saves = append(saves, bf[len(bf)-1])
					}
				}
			}
			// Mod folders are big: looking for save conflicts in them
			// would walk a whole game folder on every refresh.
			conflicts := a.conflictCounts(saves)
			inside := nestedIn(bf)
			ast := accounts.Load()
			for _, f := range fs {
				if f.ID != meta.FolderID {
					if _, _, ok := accounts.ParseFolderID(f.ID); ok && accounts.InVault(f.Path) {
						continue // another account's saves, waiting in the vault (see Accounts)
					}
					v := a.syncedView(ctx, c, f, s, installed)
					if g, acc, ok := accounts.ParseFolderID(f.ID); ok {
						v.Split, v.Account, v.Game = true, acc, g
						if r, ok := ast.Split(g); ok {
							// Look the game up by its own name, not "Game (Alice)".
							v.Label, v.Installed = r.Label, installed(r.Label)
						}
					}
					v.Conflicts = conflicts[f.ID]
					v.Inside = inside[f.ID]
					out = append(out, v)
				}
			}
		}
	}
	if err != nil && len(s.BackupOnly) == 0 {
		return nil, err
	}
	for _, lf := range s.BackupOnly {
		v := FolderView{ID: lf.ID, Label: lf.Label, Path: lf.Path, State: "backup-only", Backup: !s.NoBackup[lf.ID], Installed: true}
		if !v.Backup {
			v.State = "off" // kept in the list, neither synced nor backed up
		}
		if fi, err := os.Stat(lf.Path); err == nil {
			v.Exists, v.Modified = true, fi.ModTime()
		}
		out = append(out, v)
	}
	od := paths.OneDriveRoots()
	for i := range out {
		if _, ok := s.Launchers[out[i].ID]; ok {
			out[i].Kind, out[i].Installed = "launcher", true
			continue // a launcher's own data, not a game's saves
		}
		if mf, ok := s.Mods[out[i].ID]; ok {
			out[i].Kind, out[i].ModGame, out[i].ModRole, out[i].Installed = mf.Kind, mf.Game, mf.Role, true
			a.modView(&out[i])
			continue // not a save folder: Steam Cloud and OneDrive checks don't apply
		}
		out[i].OneDrive = paths.WithinAny(od, out[i].Path)
		c := discover.Classify(out[i].Label, out[i].Path)
		out[i].SteamCloud, out[i].CopyOf, out[i].UbisoftCloud = c.SteamCloud, c.CopyOf, c.UbisoftCloud
		out[i].OneDriveCopy, out[i].OneDriveCopyNewer = c.OneDriveCopy, c.OneDriveCopyNewer
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	a.addDetails(out, s)
	return out, nil
}

func (a *App) syncedView(ctx context.Context, c *syncthing.Client, f syncthing.Folder, s store.Settings, installed func(string) bool) FolderView {
	v := FolderView{ID: f.ID, Label: f.Label, Path: f.Path, Backup: !s.NoBackup[f.ID], Sync: true, Shared: len(f.Devices) - 1}
	if v.Label == "" {
		v.Label = f.ID
	}
	v.Installed = installed(v.Label)
	if fi, err := os.Stat(f.Path); err == nil {
		v.Exists, v.Modified = true, fi.ModTime()
	}
	if st, err := c.FolderStatus(ctx, f.ID); err == nil {
		v.State, v.Bytes, v.Files, v.NeedBytes = st.State, st.LocalBytes, st.GlobalFiles, st.NeedBytes
		v.Errors = max(st.Errors, st.PullErrors)
		v.Problem = folderIssue(ctx, c, f.ID, st)
		v.Repairable = isLauncherData(s, f.ID) && needsRepair(f, st)
	}
	if f.Paused {
		v.State = "paused"
	}
	return v
}

// ScanGames looks for save folders on this PC.
func (a *App) ScanGames(refresh bool) ([]GameView, error) {
	es, err := discover.Manifest(refresh)
	if err != nil {
		return nil, err
	}
	if refresh {
		mods.Forget()
	}
	found := append(discover.Scan(es), modFounds(store.LoadSettings())...)
	a.mu.Lock()
	a.lastScan = found
	a.mu.Unlock()
	return a.annotate(found), nil
}

type GameView struct {
	discover.Found
	SyncedBy  string `json:"syncedBy"`  // label of the synced or backup-only folder covering this path
	Installed bool   `json:"installed"` // the game is installed on this PC (as far as Syncer can tell)
	Dismissed bool   `json:"dismissed"` // the user removed it or stopped syncing it: never added on its own
}

func (a *App) annotate(found []discover.Found) []GameView {
	inst := cachedInstalled()
	covering := map[string]string{} // path -> label
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		fs, _ := c.Folders(ctx)
		cancel()
		for _, f := range fs {
			if f.ID != meta.FolderID {
				covering[f.Path] = cmpOr(f.Label, f.ID)
			}
		}
	}
	s := store.LoadSettings()
	for _, lf := range s.BackupOnly {
		covering[lf.Path] = lf.Label
	}
	out := make([]GameView, 0, len(found))
	for _, f := range found {
		g := GameView{Found: f, Installed: f.Kind != "" || inst.Has(f.Name), Dismissed: s.Dismissed[dismissKey(f.Path)]}
		for p, label := range covering {
			if paths.Within(p, f.Path) {
				g.SyncedBy = label
				break
			}
		}
		out = append(out, g)
	}
	return out
}

// nestedIn maps each synced folder that lies inside another synced folder to
// that folder's id. Syncer no longer creates such pairs, but older setups (or
// adopting from another PC before that was checked) left some behind.
func nestedIn(fs []backup.Folder) map[string]string {
	m := map[string]string{}
	for _, inner := range fs {
		for _, outer := range fs {
			if inner.ID != outer.ID && paths.Within(outer.Path, inner.Path) && !paths.Within(inner.Path, outer.Path) {
				m[inner.ID] = outer.ID
				break
			}
		}
	}
	return m
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ManifestUpdated returns when the game database was last refreshed (unix s).
func (a *App) ManifestUpdated() int64 {
	t := discover.ManifestAge()
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// checkNewFolder validates a folder the user wants to add and returns the ids
// already in use. A path already backed up only is returned as existing.
func checkNewFolder(path string, synced []backup.Folder) (taken map[string]bool, backupOnly string, err error) {
	return checkNewFolderWith(path, synced, paths.CheckSyncable)
}

// checkNewFolderWith is checkNewFolder with its own safety check: mod
// folders are checked by package mods instead.
func checkNewFolderWith(path string, synced []backup.Folder, check func(string) error) (taken map[string]bool, backupOnly string, err error) {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, "", errors.New("folder not found: " + path)
	}
	if err := check(path); err != nil {
		return nil, "", err
	}
	if err := overlapsSynced(path, "", synced); err != nil {
		return nil, "", err
	}
	taken = map[string]bool{}
	for _, f := range synced {
		taken[f.ID] = true
	}
	for id, lf := range store.LoadSettings().BackupOnly {
		taken[id] = true
		if lf.SyncID != "" {
			taken[lf.SyncID] = true
		}
		switch {
		case strings.EqualFold(filepath.Clean(lf.Path), path):
			backupOnly = id
		case paths.Within(lf.Path, path) || paths.Within(path, lf.Path):
			return nil, "", coveredError("overlaps \"" + lf.Label + "\", which is backed up only — remove that first")
		}
	}
	return taken, backupOnly, nil
}

// overlapsSynced says why path can't be synced when it lies in, or holds, a
// synced folder other than skipID: the same files would sync twice.
func overlapsSynced(path, skipID string, synced []backup.Folder) error {
	for _, f := range synced {
		switch {
		case f.ID == skipID:
		case paths.Within(f.Path, path):
			return coveredError("already synced as part of \"" + cmpOr(f.Label, f.ID) + "\"")
		case paths.Within(path, f.Path):
			return coveredError("this folder contains \"" + cmpOr(f.Label, f.ID) + "\", which is already synced — remove that first")
		}
	}
	return nil
}

// AddFolder starts syncing (and backing up) a save folder. A folder that is
// backed up only has its sync turned on instead.
func (a *App) AddFolder(label, path string) error {
	path = filepath.Clean(path)
	for id, lf := range store.LoadSettings().BackupOnly {
		if strings.EqualFold(filepath.Clean(lf.Path), path) {
			return a.SetFolderSync(id, true)
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	id, err := addFolder(ctx, c, label, path)
	if err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		delete(s.Ignored, id)
		delete(s.NoBackup, id)
		delete(s.Dismissed, dismissKey(path))
	})
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c) // added mid-pause: it waits too
	logx.Printf("added folder %s (%s)", label, path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// joinCtx is long enough for the safety snapshot taken before a big save
// folder joins a folder shared with other PCs.
func joinCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 35*time.Minute)
}

// AddBackupOnly backs up a save folder on this PC without syncing it.
func (a *App) AddBackupOnly(label, path string) error {
	if _, err := addBackupOnly(label, path, currentSynced()); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// currentSynced lists the synced folders, falling back to the cached list;
// none cached = nothing synced yet.
func currentSynced() []backup.Folder {
	if store.LoadSettings().SyncDisabled {
		return nil
	}
	synced, _ := syncedFolders()
	return synced
}

// addBackupOnly records a backup-only folder and returns its id.
func addBackupOnly(label, path string, synced []backup.Folder) (string, error) {
	path = filepath.Clean(path)
	taken, backupOnly, err := checkNewFolder(path, synced)
	if err != nil {
		return "", err
	}
	if backupOnly != "" {
		return "", coveredError("already backed up")
	}
	if label = strings.TrimSpace(label); label == "" {
		label = filepath.Base(path)
	}
	id := backupOnlyID(label, taken)
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.BackupOnly[id] = store.LocalFolder{ID: id, Label: label, Path: path}
		delete(s.NoBackup, id)
	}); err != nil {
		return "", err
	}
	logx.Printf("backing up %s (%s) without syncing", label, path)
	return id, nil
}

// NewFolder is a save folder to add.
type NewFolder struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// BulkResult says which folders were added and which weren't (and why).
type BulkResult struct {
	Added   []string `json:"added"`   // their paths
	Skipped []string `json:"skipped"` // "<game>: <reason>"
}

// AddBackupOnlyMany backs up several save folders without syncing them, e.g.
// every game found here that isn't installed.
func (a *App) AddBackupOnlyMany(items []NewFolder) (BulkResult, error) {
	var r BulkResult
	if len(items) > 500 {
		return r, errors.New("too many folders at once")
	}
	synced := currentSynced()
	for _, it := range items {
		label := cmpOr(strings.TrimSpace(it.Label), filepath.Base(it.Path))
		if _, err := addBackupOnly(label, it.Path, synced); err != nil {
			r.Skipped = append(r.Skipped, label+": "+err.Error())
			continue
		}
		r.Added = append(r.Added, filepath.Clean(it.Path))
	}
	if len(r.Added) > 0 {
		runtime.EventsEmit(a.ctx, "changed")
	}
	return r, nil
}

// backupOnlyID gives a backup-only folder its own id, unique to this PC: other
// PCs may still sync the same game, and their (different) saves must not land
// in the same backup folder in Drive.
func backupOnlyID(label string, taken map[string]bool) string {
	base := meta.NewID(label, nil) + "--" + hostSuffix()
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// hostSuffix names this PC in its backup-only ids.
func hostSuffix() string {
	host, _ := os.Hostname()
	h := meta.NewID(host, nil)
	if len(h) > 15 {
		h = strings.TrimRight(h[:15], "-")
	}
	return h
}

// share adds a Syncthing folder shared with every paired PC and turns syncing
// back on if "Undo everything" had turned it off. backupOff keeps the game out
// of the backup.
func (a *App) share(ctx context.Context, c *syncthing.Client, id, label, path string, backupOff bool) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return err
	}
	var synced []backup.Folder
	for _, f := range fs {
		if f.ID != meta.FolderID {
			synced = append(synced, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
		}
	}
	if err := overlapsSynced(path, id, synced); err != nil {
		return err
	}
	ds, _ := c.Devices(ctx)
	var others []string
	for _, d := range ds {
		if d.DeviceID != st.MyID {
			others = append(others, d.DeviceID)
		}
	}
	if err := meta.AddFolder(ctx, c, id, label, path, st.MyID, others); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		delete(s.Ignored, id)
		if backupOff {
			s.NoBackup[id] = true
		} else {
			delete(s.NoBackup, id)
		}
		delete(s.Dismissed, dismissKey(path))
	})
	enableSync()
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c) // added mid-pause: it waits too
	logx.Printf("added folder %s (%s)", label, path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// SetFolderSync turns syncing for one game on or off on this PC. Off keeps
// it as a backup-only folder and never touches its files. Whether it is
// backed up carries over both ways, so a game with both toggles off stays off
// whichever one the user flips first.
func (a *App) SetFolderSync(id string, on bool) error {
	if applying.has(id) {
		return errApplying
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	if on {
		s := store.LoadSettings()
		lf, ok := s.BackupOnly[id]
		if !ok {
			return errors.New("unknown folder")
		}
		if mf, mod := s.Mods[id]; mod {
			return a.resyncMod(ctx, c, id, lf, mf)
		}
		if err := paths.CheckSyncable(lf.Path); err != nil {
			return err
		}
		fs, err := c.Folders(ctx)
		if err != nil {
			return err
		}
		sid := lf.SyncID
		if sid == "" {
			taken := map[string]bool{}
			for _, f := range fs {
				taken[f.ID] = true
			}
			me := ""
			if st, err := c.Status(ctx); err == nil {
				me = st.MyID
			}
			sid = syncID(me, lf.Label, lf.Path, taken)
		}
		if err := a.share(ctx, c, sid, lf.Label, lf.Path, s.NoBackup[id]); err != nil {
			return err
		}
		_ = forgetBackup(id, false) // backups continue under the synced id
		_, err = store.UpdateSettings(func(s *store.Settings) {
			delete(s.BackupOnly, id)
			delete(s.NoBackup, id)
		})
		runtime.EventsEmit(a.ctx, "changed")
		return err
	}

	if err := splitGuard(id); err != nil {
		return err
	}
	if _, err := a.stopSync(ctx, c, id); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// stopSync stops syncing a game on this PC, keeps it as a backup-only folder
// (with its backup history) and returns its new id. Its files stay as they are.
func (a *App) stopSync(ctx context.Context, c *syncthing.Client, id string) (string, error) {
	f, err := findFolder(ctx, c, id)
	if err != nil {
		return "", err
	}
	if !paths.ValidID(f.ID) {
		return "", errors.New("this folder's id can't be used for a backup; remove it instead")
	}
	label := cmpOr(f.Label, f.ID)
	if err := c.RemoveFolder(ctx, f.ID); err != nil {
		return "", err
	}
	cleanMarkers(f.Path)
	var bid string
	s, err := store.UpdateSettings(func(s *store.Settings) { bid = toBackupOnly(s, f.ID, label, f.Path) })
	if err != nil {
		return "", err
	}
	keepHistory(a.ctx, s, f.ID, bid)
	_ = forgetBackup(f.ID, false) // this PC no longer backs up the synced id
	dropModState(ctx, c, f.ID)
	_, _ = meta.Reconcile(ctx, c)
	if s.NoBackup[bid] {
		logx.Printf("stopped syncing %s; it isn't backed up either", label)
	} else {
		logx.Printf("stopped syncing %s; still backing it up", label)
	}
	return bid, nil
}

// toBackupOnly records a synced folder as backup-only on this PC and returns
// its new id. A game whose backup was off is now neither synced nor backed up.
func toBackupOnly(s *store.Settings, syncID, label, path string) string {
	taken := map[string]bool{}
	for k := range s.BackupOnly {
		taken[k] = true
	}
	bid := backupOnlyID(label, taken)
	s.BackupOnly[bid] = store.LocalFolder{ID: bid, Label: label, Path: path, SyncID: syncID}
	if l, ok := s.Launchers[syncID]; ok {
		s.Launchers[bid] = l // kept under syncID too, for syncing it again
	}
	if mf, ok := s.Mods[syncID]; ok {
		s.Mods[bid] = mf // syncing it again needs to know it's a mod folder
		delete(s.Mods, syncID)
	}
	s.Ignored[syncID] = true
	s.Dismissed[dismissKey(path)] = true // auto-add must not sync it again
	if s.NoBackup[syncID] {
		s.NoBackup[bid] = true
	}
	delete(s.NoBackup, syncID)
	return bid
}

// enableSync turns syncing back on after "Undo everything", including the
// background task that keeps Syncthing running and applies other PCs' changes.
func enableSync() {
	if !store.LoadSettings().SyncDisabled {
		return
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) { s.SyncDisabled = false })
	ensureBackgroundTask()
	logx.Printf("syncing turned back on")
}

func findFolder(ctx context.Context, c *syncthing.Client, id string) (syncthing.Folder, error) {
	fs, err := c.Folders(ctx)
	if err != nil {
		return syncthing.Folder{}, err
	}
	for _, f := range fs {
		if f.ID == id && id != meta.FolderID {
			return f, nil
		}
	}
	return syncthing.Folder{}, errors.New("unknown folder")
}

// RemoveFolder takes a game out of Syncer on this PC: it stops syncing and
// backing it up and removes Syncthing's marker files. Save files are never
// deleted; with deleteBackup its Drive backup and history go too.
func (a *App) RemoveFolder(id string, deleteBackup bool) error {
	if applying.has(id) {
		return errApplying
	}
	if err := splitGuard(id); err != nil {
		return err
	}
	if lf, ok := store.LoadSettings().BackupOnly[id]; ok {
		if err := forgetBackup(id, deleteBackup); err != nil {
			return err
		}
		if _, err := store.UpdateSettings(func(s *store.Settings) {
			delete(s.BackupOnly, id)
			delete(s.NoBackup, id)
			delete(s.Mods, id)
			s.Dismissed[dismissKey(lf.Path)] = true
		}); err != nil {
			return err
		}
		logx.Printf("removed %s", lf.Label)
		runtime.EventsEmit(a.ctx, "changed")
		return nil
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
	// Hold the backup lock from here on: once the folder is gone from the
	// list, a failed deletion couldn't be retried from Syncer.
	unlock, err := backup.Lock()
	if err != nil && deleteBackup {
		return err
	}
	if err == nil {
		defer func() {
			if unlock != nil {
				unlock()
			}
		}()
	} else {
		unlock = nil
	}
	target, ok := backupTarget(store.LoadSettings())
	if deleteBackup && !ok {
		return errors.New("the backup folder isn't reachable, so the backup can't be deleted right now")
	}
	if err := c.RemoveFolder(ctx, id); err != nil {
		return err
	}
	cleanMarkers(f.Path)
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		s.Ignored[id] = true
		s.Dismissed[dismissKey(f.Path)] = true
		delete(s.NoBackup, id)
		delete(s.Mods, id)
	})
	dropModState(ctx, c, id)
	if unlock != nil && paths.ValidID(id) {
		err := backup.Forget(target, id, deleteBackup)
		unlock()
		unlock = nil
		if err != nil {
			_, _ = meta.Reconcile(ctx, c)
			runtime.EventsEmit(a.ctx, "changed")
			return fmt.Errorf("stopped syncing, but the backup wasn't deleted: %w", err)
		}
	}
	// Not while holding the backup lock: reconciling may carry out another
	// PC's split, which needs that lock.
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("removed %s (backup deleted: %v)", cmpOr(f.Label, id), deleteBackup)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// keepHistory copies a game's backup history to its new backup-only id.
func keepHistory(ctx context.Context, s store.Settings, from, to string) {
	target, ok := backupTarget(s)
	if !ok {
		return
	}
	if err := backup.CopyHistory(ctx, target, from, to); err != nil {
		logx.Printf("keep backup history of %s: %v", from, err)
	}
}

// forgetBackup drops a folder's backup bookkeeping and, if asked, its backup.
func forgetBackup(id string, deleteBackup bool) error {
	if !paths.ValidID(id) {
		return nil // never backed up
	}
	target, ok := backupTarget(store.LoadSettings())
	if deleteBackup && !ok {
		return errors.New("the backup folder isn't reachable, so the backup can't be deleted right now")
	}
	unlock, err := backup.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return backup.Forget(target, id, deleteBackup)
}

// cleanMarkers removes what Syncthing adds to a synced folder, when that is
// safe: the .stfolder marker, .stversions only if it's empty, and .stignore
// if it holds nothing but Syncer's own lines. It returns the .stversions path
// when older versions were kept.
func cleanMarkers(dir string) string {
	if ig := filepath.Join(dir, ".stignore"); len(mergeIgnores(readIgnores(ig), nil)) == 0 {
		_ = os.Remove(ig)
	}
	marker := filepath.Join(dir, ".stfolder")
	if ms, _ := filepath.Glob(filepath.Join(marker, "syncthing-folder-*.txt")); len(ms) > 0 {
		for _, m := range ms {
			_ = os.Remove(m)
		}
	}
	_ = os.Remove(marker) // fails harmlessly if something else is in there
	v := filepath.Join(dir, ".stversions")
	if err := os.Remove(v); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return v
	}
	return ""
}

// SetFolderBackup turns the backup of one game on or off. A game that isn't
// synced either stays listed as "off": nothing happens to it until one of
// its toggles is turned back on.
func (a *App) SetFolderBackup(id string, on bool) error {
	_, err := store.UpdateSettings(func(s *store.Settings) {
		if on {
			delete(s.NoBackup, id)
		} else {
			s.NoBackup[id] = true
		}
	})
	if err == nil {
		runtime.EventsEmit(a.ctx, "changed")
	}
	return err
}

// LeaveToSteamCloud stops syncing every game Steam Cloud also keeps on this
// PC: with two sync tools on the same saves Steam asks which copy to keep,
// and the wrong pick overwrites a save. They stay backed up (or off, if
// their backup was off), and aren't synced here again on their own.
func (a *App) LeaveToSteamCloud() (int, error) {
	n, err := a.stopSyncWhere("leave to Steam Cloud", func(c discover.Class) bool { return c.SteamCloud })
	if n > 0 {
		logx.Printf("left %d game(s) to Steam Cloud", n)
	}
	return n, err
}

// StopSyncingCopies stops syncing every emulator folder that only copies
// saves the game keeps in its own folder, which is synced already: the
// same saves would sync twice. They stay backed up (or off, if their backup
// was off), and aren't synced here again on their own.
func (a *App) StopSyncingCopies() (int, error) {
	n, err := a.stopSyncWhere("stop syncing copy", func(c discover.Class) bool { return c.CopyOf != "" })
	if n > 0 {
		logx.Printf("stopped syncing %d emulator copies", n)
	}
	return n, err
}

// stopSyncWhere stops syncing every folder whose classification matches.
func (a *App) stopSyncWhere(what string, match func(discover.Class) bool) (int, error) {
	c, err := a.client()
	if err != nil {
		return 0, err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	fs, err := c.Folders(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, f := range fs {
		if f.ID == meta.FolderID || splitGuard(f.ID) != nil || !match(discover.Classify(cmpOr(f.Label, f.ID), f.Path)) {
			continue
		}
		if _, err := a.stopSync(ctx, c, f.ID); err != nil {
			logx.Printf("%s %s: %v", what, cmpOr(f.Label, f.ID), err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", cmpOr(f.Label, f.ID), err)
			}
			continue
		}
		n++
	}
	if n > 0 {
		runtime.EventsEmit(a.ctx, "changed")
	}
	return n, firstErr
}

// ---- other PCs ---------------------------------------------------------------

type AvailableView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	From   string `json:"from"`
	Reason string `json:"reason"` // a meta.Skip* reason, or meta.Pending
	Kind    string `json:"kind"`    // mod folders: mods.Kind*
	ModGame string `json:"modGame"` // … and the mod manager's game id
}

// Available lists games your other PCs sync that this PC doesn't.
func (a *App) Available() ([]AvailableView, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	av, err := meta.Available(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]AvailableView, 0, len(av))
	for _, v := range av {
		out = append(out, AvailableView{ID: v.ID, Label: cmpOr(v.Label, v.ID), Path: v.Path, From: v.From, Reason: v.Reason, Kind: v.Kind, ModGame: v.ModGame})
	}
	return out, nil
}

// SyncAvailable starts syncing a game from another PC here, even when
// "only installed games" would skip it.
func (a *App) SyncAvailable(id string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := joinCtx(a.ctx)
	defer cancel()
	av, err := meta.Available(ctx, c)
	if err != nil {
		return err
	}
	for _, v := range av {
		if v.ID == id {
			if v.Kind != "" {
				return a.joinModAvailable(ctx, c, v)
			}
			return a.share(ctx, c, v.ID, cmpOr(v.Label, v.ID), v.Path, false)
		}
	}
	return errors.New("that game is no longer offered by your other PCs")
}

// RemoveUninstalled stops syncing games that aren't installed on this PC.
// They aren't marked as removed, so they come back once the game is installed
// (auto-add and other PCs' folders both skip games that aren't installed
// while "only installed games" is on).
func (a *App) RemoveUninstalled() (int, error) {
	if !store.LoadSettings().InstalledOnly {
		return 0, nil
	}
	c, err := a.client()
	if err != nil {
		return 0, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	fs, err := c.Folders(ctx)
	if err != nil {
		return 0, err
	}
	inst := cachedInstalled()
	s := store.LoadSettings()
	n := 0
	for _, f := range fs {
		// Not converted to backup-only: the PCs that have the game installed
		// keep backing these saves up into the same Drive folder. Mod
		// folders only exist where the mod manager has the game.
		if f.ID == meta.FolderID || isMod(s, f.ID) || isLauncherData(s, f.ID) || splitGuard(f.ID) != nil || inst.Has(cmpOr(f.Label, f.ID)) {
			continue
		}
		if err := c.RemoveFolder(ctx, f.ID); err != nil {
			return n, err
		}
		cleanMarkers(f.Path)
		if paths.ValidID(f.ID) {
			_ = forgetBackup(f.ID, false) // its backup stays; this PC's bookkeeping goes
		}
		n++
	}
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("stopped syncing %d games that aren't installed", n)
	runtime.EventsEmit(a.ctx, "changed")
	return n, nil
}

// PickFolder opens a native folder picker.
func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose a save folder", DefaultDirectory: paths.Root(paths.Home)})
}

// OpenPath shows a folder in Explorer. Only existing directories are opened,
// so the page can't make Explorer launch a file.
func (a *App) OpenPath(p string) {
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return
	}
	_ = exec.Command(winx.WindowsDir("explorer.exe"), p).Start()
}
