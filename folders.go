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

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
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
	Backup    bool      `json:"backup"`
	Sync      bool      `json:"sync"`      // false = backed up here but not synced
	Installed bool      `json:"installed"` // only meaningful with "only installed games" on
	Exists    bool      `json:"exists"`
	Shared    int       `json:"shared"`
	Modified  time.Time `json:"modified"`
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
			for _, f := range fs {
				if f.ID != meta.FolderID {
					out = append(out, a.syncedView(ctx, c, f, s, installed))
				}
			}
		}
	}
	if err != nil && len(s.BackupOnly) == 0 {
		return nil, err
	}
	for _, lf := range s.BackupOnly {
		v := FolderView{ID: lf.ID, Label: lf.Label, Path: lf.Path, State: "backup-only", Backup: true, Installed: true}
		if fi, err := os.Stat(lf.Path); err == nil {
			v.Exists, v.Modified = true, fi.ModTime()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
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
		v.State, v.Bytes, v.Files, v.NeedBytes, v.Errors = st.State, st.LocalBytes, st.GlobalFiles, st.NeedBytes, st.Errors+st.PullErrors
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
	found := discover.Scan(es)
	a.mu.Lock()
	a.lastScan = found
	a.mu.Unlock()
	return a.annotate(found), nil
}

type GameView struct {
	discover.Found
	SyncedBy string `json:"syncedBy"` // label of the synced or backup-only folder covering this path
}

func (a *App) annotate(found []discover.Found) []GameView {
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
	for _, lf := range store.LoadSettings().BackupOnly {
		covering[lf.Path] = lf.Label
	}
	out := make([]GameView, 0, len(found))
	for _, f := range found {
		g := GameView{Found: f}
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
func (a *App) checkNewFolder(ctx context.Context, path string, synced []syncthing.Folder) (taken map[string]bool, backupOnly string, err error) {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, "", errors.New("folder not found: " + path)
	}
	if err := paths.CheckSyncable(path); err != nil {
		return nil, "", err
	}
	taken = map[string]bool{}
	for _, f := range synced {
		taken[f.ID] = true
		if paths.Within(f.Path, path) {
			return nil, "", errors.New("already synced as part of \"" + cmpOr(f.Label, f.ID) + "\"")
		}
		if paths.Within(path, f.Path) && f.ID != meta.FolderID {
			return nil, "", errors.New("this folder contains \"" + cmpOr(f.Label, f.ID) + "\", which is already synced — remove that first")
		}
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
			return nil, "", errors.New("overlaps \"" + lf.Label + "\", which is backed up only — remove that first")
		}
	}
	return taken, backupOnly, nil
}

// AddFolder starts syncing (and backing up) a save folder.
func (a *App) AddFolder(label, path string) error {
	path = filepath.Clean(path)
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	fs, err := c.Folders(ctx)
	if err != nil {
		return err
	}
	taken, backupOnly, err := a.checkNewFolder(ctx, path, fs)
	if err != nil {
		return err
	}
	if backupOnly != "" {
		return a.SetFolderSync(backupOnly, true)
	}
	if label == "" {
		label = filepath.Base(path)
	}
	return a.share(ctx, c, meta.NewID(label, taken), label, path)
}

// AddBackupOnly backs up a save folder on this PC without syncing it.
func (a *App) AddBackupOnly(label, path string) error {
	path = filepath.Clean(path)
	var synced []syncthing.Folder
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		synced, _ = c.Folders(ctx)
		cancel()
	}
	taken, backupOnly, err := a.checkNewFolder(a.ctx, path, synced)
	if err != nil {
		return err
	}
	if backupOnly != "" {
		return errors.New("already backed up")
	}
	if label == "" {
		label = filepath.Base(path)
	}
	id := backupOnlyID(label, taken)
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.BackupOnly[id] = store.LocalFolder{ID: id, Label: label, Path: path}
		delete(s.NoBackup, id)
	}); err != nil {
		return err
	}
	logx.Printf("backing up %s (%s) without syncing", label, path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// backupOnlyID gives a backup-only folder its own id, unique to this PC: other
// PCs may still sync the same game, and their (different) saves must not land
// in the same backup folder in Drive.
func backupOnlyID(label string, taken map[string]bool) string {
	host, _ := os.Hostname()
	h := meta.NewID(host, nil)
	if len(h) > 15 {
		h = strings.TrimRight(h[:15], "-")
	}
	base := meta.NewID(label, nil) + "--" + h
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// share adds a Syncthing folder shared with every paired PC and turns syncing
// back on if "Undo everything" had turned it off.
func (a *App) share(ctx context.Context, c *syncthing.Client, id, label, path string) error {
	st, err := c.Status(ctx)
	if err != nil {
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
		delete(s.NoBackup, id)
		s.SyncDisabled = false
	})
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("added folder %s (%s)", label, path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// SetFolderSync turns syncing for one game on or off on this PC. Off keeps
// backing it up (as a backup-only folder) and never touches its files.
func (a *App) SetFolderSync(id string, on bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	if on {
		lf, ok := store.LoadSettings().BackupOnly[id]
		if !ok {
			return errors.New("unknown folder")
		}
		if err := paths.CheckSyncable(lf.Path); err != nil {
			return err
		}
		fs, err := c.Folders(ctx)
		if err != nil {
			return err
		}
		syncID := lf.SyncID
		if syncID == "" {
			taken := map[string]bool{}
			for _, f := range fs {
				taken[f.ID] = true
			}
			syncID = meta.NewID(lf.Label, taken)
		}
		if err := a.share(ctx, c, syncID, lf.Label, lf.Path); err != nil {
			return err
		}
		_ = forgetBackup(id, false) // backups continue under the synced id
		_, err = store.UpdateSettings(func(s *store.Settings) { delete(s.BackupOnly, id) })
		runtime.EventsEmit(a.ctx, "changed")
		return err
	}

	f, err := findFolder(ctx, c, id)
	if err != nil {
		return err
	}
	if !paths.ValidID(f.ID) {
		return errors.New("this folder's id can't be used for a backup; remove it instead")
	}
	label := cmpOr(f.Label, f.ID)
	if err := c.RemoveFolder(ctx, f.ID); err != nil {
		return err
	}
	cleanMarkers(f.Path)
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		taken := map[string]bool{}
		for k := range s.BackupOnly {
			taken[k] = true
		}
		bid := backupOnlyID(label, taken)
		s.BackupOnly[bid] = store.LocalFolder{ID: bid, Label: label, Path: f.Path, SyncID: f.ID}
		s.Ignored[f.ID] = true
		delete(s.NoBackup, f.ID)
	}); err != nil {
		return err
	}
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("stopped syncing %s; still backing it up", label)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
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
	if lf, ok := store.LoadSettings().BackupOnly[id]; ok {
		if err := forgetBackup(id, deleteBackup); err != nil {
			return err
		}
		if _, err := store.UpdateSettings(func(s *store.Settings) {
			delete(s.BackupOnly, id)
			delete(s.NoBackup, id)
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
	if err := forgetBackup(id, deleteBackup); err != nil {
		return err
	}
	if err := c.RemoveFolder(ctx, id); err != nil {
		return err
	}
	cleanMarkers(f.Path)
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		s.Ignored[id] = true
		delete(s.NoBackup, id)
	})
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("removed %s (backup deleted: %v)", cmpOr(f.Label, id), deleteBackup)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// forgetBackup drops a folder's backup bookkeeping and, if asked, its backup.
func forgetBackup(id string, deleteBackup bool) error {
	if !paths.ValidID(id) {
		return nil // never backed up
	}
	target, ok := backup.Target(store.LoadSettings().BackupRoot)
	if deleteBackup && !ok {
		return errors.New("the backup folder isn't reachable, so the backup can't be deleted right now")
	}
	return backup.Forget(target, id, deleteBackup)
}

// cleanMarkers removes what Syncthing adds to a synced folder, when that is
// safe: the .stfolder marker, and .stversions only if it's empty. It returns
// the .stversions path when older versions were kept.
func cleanMarkers(dir string) string {
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

func (a *App) SetFolderBackup(id string, on bool) error {
	if _, ok := store.LoadSettings().BackupOnly[id]; ok && !on {
		return errors.New("this game is only backed up; remove it instead")
	}
	_, err := store.UpdateSettings(func(s *store.Settings) {
		if on {
			delete(s.NoBackup, id)
		} else {
			s.NoBackup[id] = true
		}
	})
	return err
}

// ---- other PCs ---------------------------------------------------------------

type AvailableView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	From   string `json:"from"`
	Reason string `json:"reason"` // removed | not-installed
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
		out = append(out, AvailableView{ID: v.ID, Label: cmpOr(v.Label, v.ID), Path: v.Path, From: v.From, Reason: v.Reason})
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
	ctx, cancel := a.callCtx()
	defer cancel()
	av, err := meta.Available(ctx, c)
	if err != nil {
		return err
	}
	for _, v := range av {
		if v.ID == id {
			return a.share(ctx, c, v.ID, cmpOr(v.Label, v.ID), v.Path)
		}
	}
	return errors.New("that game is no longer offered by your other PCs")
}

// RemoveUninstalled stops syncing games that aren't installed on this PC.
// They aren't marked as removed, so they come back once the game is installed.
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
	n := 0
	for _, f := range fs {
		if f.ID == meta.FolderID || inst.Has(cmpOr(f.Label, f.ID)) {
			continue
		}
		if err := c.RemoveFolder(ctx, f.ID); err != nil {
			return n, err
		}
		cleanMarkers(f.Path)
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
