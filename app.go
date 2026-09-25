package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// App is bound to the frontend; every exported method is callable from JS.
type App struct {
	ctx context.Context

	mu        sync.Mutex
	backingUp bool
	cancel    context.CancelFunc
	lastScan  []discover.Found
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.applyTheme(store.LoadSettings().Theme)
	go func() {
		migrateLegacy()
		ensureBackgroundTask()
		if c, err := syncthing.New(); err == nil {
			if _, err := meta.Reconcile(ctx, c); err == nil {
				runtime.EventsEmit(ctx, "changed")
			}
		}
		go a.autoAddLoop(ctx)
		a.watch(ctx)
	}()
}

// watch forwards Syncthing activity to the UI and keeps metadata reconciled
// while the window is open (so pairing completes without waiting for the
// background task).
func (a *App) watch(ctx context.Context) {
	since := 0
	lastReconcile := time.Time{}
	for ctx.Err() == nil {
		c, err := syncthing.New()
		if err != nil {
			sleep(ctx, 5*time.Second)
			continue
		}
		evs, err := c.Events(ctx, since,
			"StateChanged,FolderCompletion,DeviceConnected,DeviceDisconnected,PendingDevicesChanged,PendingFoldersChanged,ConfigSaved,FolderSummary,RemoteIndexUpdated,LocalIndexUpdated")
		if err != nil {
			runtime.EventsEmit(ctx, "changed")
			sleep(ctx, 5*time.Second)
			continue
		}
		reconcile := false
		for _, e := range evs {
			since = e.ID
			switch e.Type {
			case "PendingFoldersChanged", "DeviceConnected", "RemoteIndexUpdated":
				reconcile = true
			}
		}
		if reconcile || time.Since(lastReconcile) > time.Minute {
			if rep, err := meta.Reconcile(ctx, c); err == nil {
				lastReconcile = time.Now()
				if len(rep.Added) > 0 {
					runtime.EventsEmit(ctx, "toast", "Added from another PC: "+strings.Join(rep.Added, ", "))
				}
			}
		}
		if len(evs) > 0 {
			runtime.EventsEmit(ctx, "changed")
		}
	}
}

// autoAddLoop picks up newly installed games while the window is open.
func (a *App) autoAddLoop(ctx context.Context) {
	for ctx.Err() == nil {
		a.runAutoAdd()
		sleep(ctx, time.Hour)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (a *App) client() (*syncthing.Client, error) { return syncthing.New() }

func (a *App) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, 20*time.Second)
}

// ---- overview ----------------------------------------------------------------

type SyncthingInfo struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	MyID      string `json:"myID"`
	GUI       string `json:"gui"`
	Error     string `json:"error"`
}

type Overview struct {
	Syncthing  SyncthingInfo    `json:"syncthing"`
	Devices    int              `json:"devices"`
	Online     int              `json:"online"`
	Pending    int              `json:"pending"`
	Folders    int              `json:"folders"`
	Syncing    int              `json:"syncing"`
	Errors     int              `json:"errors"`
	Drive      backup.DriveInfo `json:"drive"`
	Target     string           `json:"target"`
	LastBackup *store.BackupRun `json:"lastBackup"`
	BackingUp  bool             `json:"backingUp"`
	Settings   store.Settings   `json:"settings"`
}

func (a *App) Overview() Overview {
	s := store.LoadSettings()
	o := Overview{Settings: s, Drive: backup.DetectDrive(s.DriveRoot), LastBackup: store.LoadState().LastBackup}
	o.Target, _ = backupTarget(s)
	o.Syncthing.Installed = syncthing.FindExe() != ""
	a.mu.Lock()
	o.BackingUp = a.backingUp
	a.mu.Unlock()
	if !o.BackingUp {
		o.BackingUp = backup.Running()
	}
	c, err := a.client()
	if err != nil {
		return o
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		if !errors.Is(err, syncthing.ErrNotRunning) {
			o.Syncthing.Error = err.Error()
		}
		return o
	}
	o.Syncthing.Running, o.Syncthing.MyID, o.Syncthing.GUI = true, st.MyID, c.GUIURL()
	if ds, err := c.Devices(ctx); err == nil {
		o.Devices = len(ds) - 1
	}
	if cs, err := c.Connections(ctx); err == nil {
		for id, cn := range cs.Connections {
			if cn.Connected && id != st.MyID {
				o.Online++
			}
		}
	}
	if p, err := c.PendingDevices(ctx); err == nil {
		o.Pending = len(p)
	}
	if fs, err := c.Folders(ctx); err == nil {
		for _, f := range fs {
			if f.ID == meta.FolderID {
				continue
			}
			o.Folders++
			if fst, err := c.FolderStatus(ctx, f.ID); err == nil {
				if fst.State == "syncing" || fst.State == "sync-preparing" || fst.NeedFiles > 0 {
					o.Syncing++
				}
				if fst.Errors+fst.PullErrors > 0 || fst.State == "error" {
					o.Errors++
				}
			}
		}
	}
	return o
}

// ---- syncthing lifecycle -----------------------------------------------------

func (a *App) InstallSyncthing() error {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	if err := syncthing.Install(ctx); err != nil {
		return err
	}
	if err := ensureSyncthingTask(); err != nil {
		logx.Printf("syncthing autostart: %v", err)
	}
	return a.StartSyncthing()
}

func (a *App) StartSyncthing() error {
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	if err := syncthing.Start(ctx); err != nil {
		return err
	}
	_ = ensureSyncthingTask()
	if c, err := a.client(); err == nil {
		_, _ = meta.Reconcile(ctx, c)
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

func (a *App) OpenSyncthingGUI() {
	if c, err := a.client(); err == nil {
		runtime.BrowserOpenURL(a.ctx, c.GUIURL())
	}
}

// ---- devices -----------------------------------------------------------------

type DeviceView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Connected  bool    `json:"connected"`
	Address    string  `json:"address"`
	Completion float64 `json:"completion"`
	NeedBytes  int64   `json:"needBytes"`
}

type PendingView struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Address string    `json:"address"`
	Time    time.Time `json:"time"`
}

type DevicesView struct {
	MyID    string        `json:"myID"`
	MyName  string        `json:"myName"`
	QR      string        `json:"qr"`
	Devices []DeviceView  `json:"devices"`
	Pending []PendingView `json:"pending"`
}

func (a *App) Devices() (DevicesView, error) {
	var v DevicesView
	c, err := a.client()
	if err != nil {
		return v, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return v, err
	}
	v.MyID = st.MyID
	if png, err := qrcode.Encode(st.MyID, qrcode.Medium, 256); err == nil {
		v.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	ds, err := c.Devices(ctx)
	if err != nil {
		return v, err
	}
	conns, _ := c.Connections(ctx)
	peers := meta.Peers()
	for _, d := range ds {
		if d.DeviceID == st.MyID {
			v.MyName = d.Name
			continue
		}
		dv := DeviceView{ID: d.DeviceID, Name: d.Name}
		if n := peers[d.DeviceID]; dv.Name == "" && n != "" {
			dv.Name = n
		}
		if cn, ok := conns.Connections[d.DeviceID]; ok {
			dv.Connected, dv.Address = cn.Connected, cn.Address
		}
		if comp, err := c.Completion(ctx, d.DeviceID); err == nil {
			dv.Completion, dv.NeedBytes = comp.Completion, comp.NeedBytes
		}
		v.Devices = append(v.Devices, dv)
	}
	sort.Slice(v.Devices, func(i, j int) bool { return v.Devices[i].Name < v.Devices[j].Name })
	if p, err := c.PendingDevices(ctx); err == nil {
		for id, pd := range p {
			v.Pending = append(v.Pending, PendingView{ID: id, Name: pd.Name, Address: pd.Address, Time: pd.Time})
		}
	}
	return v, nil
}

// normalizeID accepts IDs with or without dashes/spaces, any case.
func normalizeID(id string) (string, error) {
	r := strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "", "\r", "")
	raw := strings.ToUpper(r.Replace(id))
	if len(raw) != 56 {
		return "", errors.New("that doesn't look like a device ID (expected 56 letters/digits)")
	}
	for _, ch := range raw {
		if !(ch >= 'A' && ch <= 'Z' || ch >= '2' && ch <= '7') {
			return "", errors.New("device IDs only contain letters A–Z and digits 2–7")
		}
	}
	var parts []string
	for i := 0; i < 56; i += 7 {
		parts = append(parts, raw[i:i+7])
	}
	return strings.Join(parts, "-"), nil
}

// AddDevice pairs with another PC and shares every save folder with it.
func (a *App) AddDevice(id, name string) error {
	id, err := normalizeID(id)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	if st, err := c.Status(ctx); err == nil && st.MyID == id {
		return errors.New("that's this PC's own ID — enter the ID shown on the other PC")
	}
	if name == "" {
		name = "PC " + id[:7]
	}
	if err := c.AddDevice(ctx, syncthing.Device{DeviceID: id, Name: name, Addresses: []string{"dynamic"},
		Introducer: true}); err != nil {
		return err
	}
	_ = c.DismissPendingDevice(ctx, id)
	if _, err := meta.Reconcile(ctx, c); err != nil {
		return err
	}
	logx.Printf("paired device %s (%s)", name, id[:7])
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

func (a *App) DismissDevice(id string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	return c.DismissPendingDevice(ctx, id)
}

func (a *App) RemoveDevice(id string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	if err := c.RemoveDevice(ctx, id); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

func (a *App) CopyText(s string) { _ = runtime.ClipboardSetText(a.ctx, s) }

func (a *App) PasteText() string {
	s, _ := runtime.ClipboardGetText(a.ctx)
	return s
}

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
	Exists    bool      `json:"exists"`
	Shared    int       `json:"shared"`
	Modified  time.Time `json:"modified"`
}

func (a *App) Folders() ([]FolderView, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	fs, err := c.Folders(ctx)
	if err != nil {
		return nil, err
	}
	s := store.LoadSettings()
	out := make([]FolderView, 0, len(fs))
	for _, f := range fs {
		if f.ID == meta.FolderID {
			continue
		}
		v := FolderView{ID: f.ID, Label: f.Label, Path: f.Path, Backup: !s.NoBackup[f.ID], Shared: len(f.Devices) - 1}
		if v.Label == "" {
			v.Label = f.ID
		}
		if fi, err := os.Stat(f.Path); err == nil {
			v.Exists, v.Modified = true, fi.ModTime()
		}
		if st, err := c.FolderStatus(ctx, f.ID); err == nil {
			v.State, v.Bytes, v.Files, v.NeedBytes, v.Errors = st.State, st.LocalBytes, st.GlobalFiles, st.NeedBytes, st.Errors+st.PullErrors
		}
		if f.Paused {
			v.State = "paused"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out, nil
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
	SyncedBy string `json:"syncedBy"` // folder id/label covering this path
}

func (a *App) annotate(found []discover.Found) []GameView {
	var folders []syncthing.Folder
	if c, err := a.client(); err == nil {
		ctx, cancel := a.callCtx()
		folders, _ = c.Folders(ctx)
		cancel()
	}
	out := make([]GameView, 0, len(found))
	for _, f := range found {
		g := GameView{Found: f}
		for _, sf := range folders {
			if sf.ID != meta.FolderID && paths.Within(sf.Path, f.Path) {
				g.SyncedBy = sf.Label
				if g.SyncedBy == "" {
					g.SyncedBy = sf.ID
				}
				break
			}
		}
		out = append(out, g)
	}
	return out
}

// ManifestUpdated returns when the game database was last refreshed (unix s).
func (a *App) ManifestUpdated() int64 {
	t := discover.ManifestAge()
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// AddFolder starts syncing (and backing up) a save folder.
func (a *App) AddFolder(label, path string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
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
	_, _ = meta.Reconcile(ctx, c)
	logx.Printf("added folder %s (%s)", label, path)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// PickFolder opens a native folder picker.
func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose a save folder", DefaultDirectory: paths.Root(paths.Home)})
}

// RemoveFolder stops syncing a folder on this PC. Files are never deleted.
func (a *App) RemoveFolder(id string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	var path string
	if fs, err := c.Folders(ctx); err == nil {
		for _, f := range fs {
			if f.ID == id {
				path = f.Path
			}
		}
	}
	if err := c.RemoveFolder(ctx, id); err != nil {
		return err
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		s.Ignored[id] = true
		if path != "" {
			s.Dismissed[dismissKey(path)] = true
		}
	})
	_, _ = meta.Reconcile(ctx, c)
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

func (a *App) SetFolderBackup(id string, on bool) error {
	_, err := store.UpdateSettings(func(s *store.Settings) {
		if on {
			delete(s.NoBackup, id)
		} else {
			s.NoBackup[id] = true
		}
	})
	return err
}

func (a *App) OpenPath(p string) {
	_ = exec.Command("explorer.exe", p).Start()
}

// ---- backup ------------------------------------------------------------------

// BackupNow runs a backup in the background, streaming "backup:progress" and
// finishing with "backup:done".
func (a *App) BackupNow() error {
	a.mu.Lock()
	if a.backingUp {
		a.mu.Unlock()
		return backup.ErrBusy
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.backingUp, a.cancel = true, cancel
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.backingUp, a.cancel = false, nil
			a.mu.Unlock()
			cancel()
		}()
		last := time.Time{}
		res, err := runBackup(ctx, func(p backup.Progress) {
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				runtime.EventsEmit(a.ctx, "backup:progress", p)
			}
		})
		if err != nil {
			runtime.EventsEmit(a.ctx, "backup:done", map[string]any{"error": err.Error()})
			return
		}
		runtime.EventsEmit(a.ctx, "backup:done", res)
	}()
	return nil
}

func (a *App) CancelBackup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) RestorePoints(id string) []int64 {
	t, ok := backupTarget(store.LoadSettings())
	if !ok {
		return nil
	}
	var out []int64
	for _, p := range backup.Points(t, id) {
		out = append(out, p.Unix())
	}
	return out
}

// Restore copies a backup back into place. point 0 = latest backup.
func (a *App) Restore(id string, point int64) (int, error) {
	t, ok := backupTarget(store.LoadSettings())
	if !ok {
		return 0, errors.New("Google Drive folder not found")
	}
	fs, err := backupFolders()
	if err != nil {
		return 0, err
	}
	for _, f := range fs {
		if f.ID == id {
			var at time.Time
			if point > 0 {
				at = time.Unix(point, 0)
			}
			n, err := backup.Restore(t, f, at)
			if err == nil {
				logx.Printf("restored %d files into %s", n, f.Label)
			}
			return n, err
		}
	}
	return 0, errors.New("unknown folder")
}

func (a *App) OpenBackupFolder() {
	if t, ok := backupTarget(store.LoadSettings()); ok {
		_ = os.MkdirAll(t, 0o755)
		a.OpenPath(t)
	}
}

func (a *App) PickBackupFolder() (string, error) {
	p, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose where backups go"})
	if err != nil || p == "" {
		return "", err
	}
	_, err = store.UpdateSettings(func(s *store.Settings) { s.BackupRoot = p })
	return p, err
}

func (a *App) Log() []string { return logx.Tail(200) }

// ---- settings ----------------------------------------------------------------

func (a *App) SaveSettings(in store.Settings) (store.Settings, error) {
	old := store.LoadSettings()
	s, err := store.UpdateSettings(func(s *store.Settings) {
		s.Theme, s.BackupEnabled, s.BackupRoot, s.DriveRoot = in.Theme, in.BackupEnabled, in.BackupRoot, in.DriveRoot
		s.IncludeSteamCloud, s.AutoAdd = in.IncludeSteamCloud, in.AutoAdd
		if in.IntervalHours > 0 {
			s.IntervalHours = in.IntervalHours
		}
		if in.KeepDays > 0 {
			s.KeepDays = in.KeepDays
		}
	})
	if err != nil {
		return s, err
	}
	a.applyTheme(s.Theme)
	ensureBackgroundTask()
	if s.AutoAdd && (!old.AutoAdd || s.IncludeSteamCloud != old.IncludeSteamCloud) {
		go a.runAutoAdd()
	}
	return s, nil
}

func (a *App) applyTheme(t string) {
	if a.ctx == nil {
		return
	}
	switch t {
	case "light":
		runtime.WindowSetLightTheme(a.ctx)
	case "dark":
		runtime.WindowSetDarkTheme(a.ctx)
	default:
		runtime.WindowSetSystemDefaultTheme(a.ctx)
	}
}
