package main

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
	"github.com/ApolloF/syncer/internal/winx"
)

// App is bound to the frontend; every exported method is callable from JS.
type App struct {
	ctx context.Context

	mu        sync.Mutex
	backingUp bool
	cancel    context.CancelFunc
	lastScan  []discover.Found
	conflicts *conflictCache
	details   map[string]backupDetail // per folder id, see addDetails
	newer     map[string]newerSave    // synced folder id -> newer save on another PC
	session   sessionTracker          // the game running, see session.go
	others    *othersCache            // backups in Drive no game here uses
	quitting  bool                    // quit from the tray: really exit
	headless  bool                    // the --api helper: no window to tell about changes
	hidden    bool                    // only the tray icon shows, no window
	updating  bool                    // installing a new release, see update.go
	tray      trayItems
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.applyTheme(store.LoadSettings().Theme)
	a.startTray()
	go func() {
		ensureAutostart(store.LoadSettings().StartAtLogin)
		migrateLegacy()
		ensureBackgroundTask()
		if c, err := syncthing.New(); err == nil {
			if _, err := meta.Reconcile(ctx, c); err == nil {
				runtime.EventsEmit(ctx, "changed")
			}
			ensureIgnores(ctx, c)
		}
		go a.autoAddLoop(ctx)
		go a.pauseLoop(ctx)
		go a.updateLoop(ctx)
		go a.notifyLoop(ctx)
		go a.newerLoop(ctx)
		go a.sessionLoop(ctx)
		go a.modsLoop(ctx)
		a.watch(ctx)
	}()
}

// watch forwards Syncthing activity to the UI and keeps metadata reconciled
// while the window is open (so pairing completes without waiting for the
// background task).
func (a *App) watch(ctx context.Context) {
	since := 0
	lastReconcile := time.Time{}
	up := true // Syncthing answered last time; the UI is told when that changes
	for ctx.Err() == nil {
		c, err := syncthing.New()
		if err != nil {
			sleep(ctx, 5*time.Second)
			continue
		}
		evs, err := c.Events(ctx, since,
			"StateChanged,FolderCompletion,DeviceConnected,DeviceDisconnected,PendingDevicesChanged,PendingFoldersChanged,ConfigSaved,FolderSummary,RemoteIndexUpdated,LocalIndexUpdated")
		if err != nil {
			if up {
				up = false
				runtime.EventsEmit(ctx, "changed")
			}
			sleep(ctx, 5*time.Second)
			continue
		}
		if !up {
			up = true
			runtime.EventsEmit(ctx, "changed")
		}
		reconcile, arrived := false, false
		for _, e := range evs {
			since = e.ID
			switch e.Type {
			case "PendingFoldersChanged", "DeviceConnected", "RemoteIndexUpdated":
				reconcile = true
			case "FolderCompletion", "LocalIndexUpdated":
				arrived = true
			}
		}
		if arrived {
			a.recheckNewer() // a newer save from another PC may be here now
		}
		if reconcile || time.Since(lastReconcile) > time.Minute {
			if rep, err := meta.Reconcile(ctx, c); err == nil {
				lastReconcile = time.Now()
				a.refreshTray() // accounts may have changed on another PC
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
	Errors     int              `json:"errors"` // folders with an issue (see Issues)
	Issues     []FolderIssue    `json:"issues"`
	Conflicts  int              `json:"conflicts"` // conflict copies across all folders
	Overlaps   int              `json:"overlaps"`  // synced folders inside another synced folder
	Drive      backup.DriveInfo `json:"drive"`
	Google     GoogleView       `json:"google"`
	Target     string           `json:"target"`
	LastBackup *store.BackupRun `json:"lastBackup"`
	BackingUp  bool             `json:"backingUp"`
	Gaming     bool             `json:"gaming"` // automatic backups are holding off right now
	Paused     bool             `json:"paused"` // syncing and automatic backups are paused (until settings.pausedUntil)
	Settings   store.Settings   `json:"settings"`
	Version    string           `json:"version"`
	Update     *UpdateInfo      `json:"update"` // a newer release, if one is out
	// VersionGaps: linked PCs running an older or newer Syncer than this one.
	VersionGaps []VersionGap `json:"versionGaps"`
}

// FolderIssue is a synced folder Syncthing has a problem with, and what it is.
type FolderIssue struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Problem string `json:"problem"`
	Mod     bool   `json:"mod"` // a mod folder: listed on the Mods page while finding mods is on
	// Launcher: a launcher's own data, listed under Settings → Launchers.
	Launcher bool `json:"launcher"`
}

func (a *App) Overview() Overview {
	s := store.LoadSettings()
	o := Overview{Settings: s, Drive: backup.DetectDrive(s.DriveRoot), LastBackup: store.LoadState().LastBackup,
		Paused: s.Paused(), Version: version, Update: availableUpdate()}
	o.Target, _ = backupTarget(s)
	o.Google = googleView(s)
	o.Syncthing.Installed = syncthing.FindExe() != ""
	a.mu.Lock()
	o.BackingUp = a.backingUp
	a.mu.Unlock()
	if !o.BackingUp {
		o.BackingUp = backup.Running()
	}
	o.Gaming = s.PauseWhileGaming && playing(cachedInstalled())
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
		peers, linked := meta.Peers(), map[string]string{}
		for _, d := range ds {
			if d.DeviceID != st.MyID {
				linked[d.DeviceID] = cmp.Or(d.Name, peers[d.DeviceID], d.DeviceID[:7])
			}
		}
		o.VersionGaps = versionGaps(st.MyID, linked)
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
		var bf []backup.Folder
		for _, f := range fs {
			if f.ID != meta.FolderID {
				bf = append(bf, backup.Folder{ID: f.ID, Label: f.Label, Path: f.Path})
			}
		}
		for id, n := range a.conflictCounts(bf) {
			if !inVault(fs, id) {
				o.Conflicts += n // other accounts' conflicts show under Accounts
			}
		}
		o.Overlaps = len(nestedIn(bf))
		for _, f := range fs {
			if f.ID == meta.FolderID || accounts.InVault(f.Path) {
				continue
			}
			if !isLauncherData(s, f.ID) {
				o.Folders++ // a launcher's own data isn't a game
			}
			if fst, err := c.FolderStatus(ctx, f.ID); err == nil {
				if fst.State == "syncing" || fst.State == "sync-preparing" || fst.NeedFiles > 0 {
					o.Syncing++
				}
				if p := folderIssue(ctx, c, f.ID, fst); p != "" {
					o.Errors++
					o.Issues = append(o.Issues, FolderIssue{ID: f.ID, Label: cmpOr(f.Label, f.ID), Problem: p, Mod: isMod(s, f.ID), Launcher: isLauncherData(s, f.ID)})
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
	Via        string  `json:"via"` // lan, direct or relay (see syncthing.Connection.Via)
	Completion float64 `json:"completion"`
	NeedBytes  int64   `json:"needBytes"`
	Version    string  `json:"version"`    // its Syncer's version, if it published one
	VersionGap string  `json:"versionGap"` // see VersionGap.Gap
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
	peers, versions := meta.Peers(), meta.PeerVersions(st.MyID)
	for _, d := range ds {
		if d.DeviceID == st.MyID {
			v.MyName = d.Name
			continue
		}
		dv := DeviceView{ID: d.DeviceID, Name: d.Name}
		if pv, ok := versions[d.DeviceID]; ok {
			dv.Version, dv.VersionGap = pv, versionGap(pv)
		}
		if n := peers[d.DeviceID]; dv.Name == "" && n != "" {
			dv.Name = n
		}
		if cn, ok := conns.Connections[d.DeviceID]; ok {
			dv.Connected, dv.Address = cn.Connected, cn.Address
			if cn.Connected {
				dv.Via = cn.Via()
			}
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
	// Introducer lets a third PC join through any already linked PC, so the
	// user never has to pair every PC with every other one.
	if err := c.AddDevice(ctx, syncthing.Device{DeviceID: id, Name: name, Addresses: []string{"dynamic"},
		Introducer: true}); err != nil {
		return err
	}
	enableSync()
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
		defer winx.BackgroundThread()() // low CPU/IO priority, even while a game runs
		defer func() {
			a.mu.Lock()
			a.backingUp, a.cancel = false, nil
			a.mu.Unlock()
			cancel()
			a.forgetDetails()
		}()
		last := time.Time{}
		res, err := runBackup(ctx, func(p backup.Progress) {
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				runtime.EventsEmit(a.ctx, "backup:progress", p)
			}
		}, nil)
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

// RestorePoint is a restore point and where it came from.
type RestorePoint struct {
	At int64 `json:"at"` // unix seconds
	backup.Origin
}

// RestorePointsView is a folder's restore points, newest first, and where
// its latest backup came from.
type RestorePointsView struct {
	Latest backup.Origin  `json:"latest"`
	Points []RestorePoint `json:"points"`
}

func (a *App) RestorePoints(id string) RestorePointsView {
	v := RestorePointsView{Points: []RestorePoint{}}
	t, ok := backupTarget(store.LoadSettings())
	if !ok {
		return v
	}
	origins, latest := backup.PointOrigins(t, id)
	v.Latest = latest
	for _, p := range backup.Points(t, id) {
		v.Points = append(v.Points, RestorePoint{At: p.Unix(), Origin: origins[p]})
	}
	return v
}

// Restore copies a backup back into place. point 0 = latest backup.
func (a *App) Restore(id string, point int64) (int, error) {
	if applying.has(id) {
		return 0, errApplying
	}
	t, ok := backupTarget(store.LoadSettings())
	if !ok {
		return 0, errors.New("Google Drive folder not found")
	}
	fs, listErr := backupFolders()
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
			a.forgetDetails() // the restore added a restore point
			return n, err
		}
	}
	if listErr != nil {
		return 0, listErr
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
	_, err = store.UpdateSettings(func(s *store.Settings) { s.BackupRoot, s.BackupBackend = p, "" })
	return p, err
}

func (a *App) Log() []string { return logx.Tail(200) }

// ---- settings ----------------------------------------------------------------

func (a *App) SaveSettings(in store.Settings) (store.Settings, error) {
	old := store.LoadSettings()
	s, err := store.UpdateSettings(func(s *store.Settings) {
		s.Theme, s.BackupEnabled, s.BackupRoot, s.DriveRoot = in.Theme, in.BackupEnabled, in.BackupRoot, in.DriveRoot
		s.IncludeSteamCloud, s.AutoAdd = in.IncludeSteamCloud, in.AutoAdd
		s.CloseToTray, s.StartAtLogin = in.CloseToTray, in.StartAtLogin
		s.PauseWhileGaming, s.InstalledOnly = in.PauseWhileGaming, in.InstalledOnly
		s.Notify, s.NoUpdateCheck = in.Notify, in.NoUpdateCheck
		s.NoCloudPull, s.NoHoldWhilePlaying = in.NoCloudPull, in.NoHoldWhilePlaying
		s.FindMods, s.AutoAddMods, s.SyncDeployedMods = in.FindMods, in.AutoAddMods, in.SyncDeployedMods
		s.ShareVortexMods = in.ShareVortexMods
		if !s.FindMods { // the experimental mod options build on finding mods
			s.AutoAddMods, s.SyncDeployedMods, s.ShareVortexMods = false, false, false
		}
		if in.ModsMaxGB > 0 || in.ModsMaxGB == -1 {
			s.ModsMaxGB = in.ModsMaxGB
		}
		if in.AutoAddMaxGB > 0 || in.AutoAddMaxGB == -1 {
			s.AutoAddMaxGB = in.AutoAddMaxGB
		}
		if in.IntervalHours > 0 {
			s.IntervalHours = in.IntervalHours
		}
		if in.KeepDays > 0 {
			s.KeepDays = in.KeepDays
		}
		if !meta.ModSettingsOf(*s).Same(meta.ModSettingsOf(old)) {
			s.ModsChanged = meta.NewModStamp() // your other PCs take it over
		}
	})
	if err != nil {
		return s, err
	}
	a.applyTheme(s.Theme)
	ensureBackgroundTask()
	if s.StartAtLogin != old.StartAtLogin {
		ensureAutostart(s.StartAtLogin)
	}
	if s.NoUpdateCheck != old.NoUpdateCheck {
		a.refreshTray()
	}
	if s.AutoAdd && (!old.AutoAdd || s.IncludeSteamCloud != old.IncludeSteamCloud || s.AutoAddMaxGB != old.AutoAddMaxGB) ||
		modsAutoOn(old, s) {
		go a.runAutoAdd()
	}
	if old.SyncDeployedMods && !s.SyncDeployedMods {
		go a.holdAllDeployed("the experimental option to sync deployed mods was turned off")
	}
	if old.ShareVortexMods != s.ShareVortexMods {
		go a.kickMods()
	}
	if !s.ModsChanged.Equal(old.ModsChanged) {
		go a.publishSettings()
	}
	return s, nil
}

// modsAutoOn reports whether adding mod folders automatically just started
// (or its size limit changed), so it should run now.
func modsAutoOn(old, s store.Settings) bool {
	return s.FindMods && s.AutoAddMods && (!(old.FindMods && old.AutoAddMods) || s.ModsMaxGB != old.ModsMaxGB)
}

// publishSettings shares a change of the mod options with the other PCs now.
func (a *App) publishSettings() {
	c, err := a.client()
	if err != nil {
		return
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	_, _ = meta.Reconcile(ctx, c)
}

// ModSettingsDiffer names your other PCs that use different mod options
// (from before they were shared).
func (a *App) ModSettingsDiffer() []string {
	c, err := a.client()
	if err != nil {
		return nil
	}
	ctx, cancel := a.callCtx()
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return nil
	}
	return meta.ModSettingsDiffer(st.MyID)
}

// UseModSettingsEverywhere makes your other PCs take this PC's mod options.
func (a *App) UseModSettingsEverywhere() error {
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.ModsChanged = meta.NewModStamp() }); err != nil {
		return err
	}
	a.publishSettings()
	return nil
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
