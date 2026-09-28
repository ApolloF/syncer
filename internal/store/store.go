// Package store persists Syncer's local settings and run state as JSON.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/paths"
)

// Settings are user choices local to this PC.
type Settings struct {
	Theme         string `json:"theme"`         // system | light | dark
	BackupEnabled bool   `json:"backupEnabled"` // scheduled Drive backup on/off
	BackupRoot    string `json:"backupRoot"`    // override for the Drive folder; "" = auto-detect
	DriveRoot     string `json:"driveRoot"`     // chosen "My Drive" when several accounts are signed in; "" = first found
	// BackupBackend is "google" when Syncer signs in to Google itself and
	// syncs the backup with Drive (no Google Drive for desktop needed).
	BackupBackend string          `json:"backupBackend,omitempty"`
	IntervalHours int             `json:"intervalHours"` // backup schedule
	KeepDays      int             `json:"keepDays"`      // how long old versions are kept
	NoBackup      map[string]bool `json:"noBackup"`      // folder ids excluded from backup
	Ignored       map[string]bool `json:"ignored"`       // folder ids never auto-added from other PCs
	// Treat games confirmed to be in Steam Cloud like any other: list them and
	// add them automatically. (JSON name kept from when it only showed them.)
	IncludeSteamCloud bool            `json:"showSteamCloud"`
	AutoAdd           bool            `json:"autoAdd"`      // sync newly detected games automatically
	AutoAddMaxGB      int             `json:"autoAddMaxGB"` // skip bigger folders when auto-adding; -1 = no limit
	Dismissed         map[string]bool `json:"dismissed"`    // portable paths the user stopped syncing; never auto-added again
	CloseToTray       bool            `json:"closeToTray"`  // closing the window keeps Syncer running in the tray
	StartAtLogin      bool            `json:"startAtLogin"` // start hidden in the tray when signing in to Windows
	Migrated          bool            `json:"migrated"`     // legacy script setup adopted

	PauseWhileGaming bool                   `json:"pauseWhileGaming"` // hold automatic backups while a game runs
	InstalledOnly    bool                   `json:"installedOnly"`    // adopt folders from other PCs only for installed games
	SyncDisabled     bool                   `json:"syncDisabled"`     // set by "Undo everything": leave Syncthing alone
	BackupOnly       map[string]LocalFolder `json:"backupOnly"`       // backed up here, not synced (by folder id); with NoBackup too: "off"

	PausedUntil   time.Time `json:"pausedUntil,omitzero"` // syncing and automatic backups are paused until then
	Notify        bool      `json:"notify"`               // Windows notifications about problems
	NoUpdateCheck bool      `json:"noUpdateCheck"`        // don't look for new Syncer releases
	NoAutoUpdate  bool      `json:"noAutoUpdate"`         // don't install new releases by themselves
	// NoCloudPull: don't take newer saves from another PC's backup by
	// yourself (see cloudpull.go); "Get it" still does.
	NoCloudPull bool `json:"noCloudPull"`
	// NoHoldWhilePlaying: keep syncing while a game runs (see session.go).
	NoHoldWhilePlaying bool `json:"noHoldWhilePlaying"`

	// Exclude lists, per save folder (keyed like Dismissed, so it survives a
	// game switching between synced and backup-only), file patterns that are
	// neither synced nor backed up on this PC.
	Exclude map[string][]string `json:"exclude,omitempty"`

	// Mods: FindMods lists mod managers' folders (Vortex) among the games
	// found on this PC, to be synced by hand. AutoAddMods (experimental)
	// syncs them without asking, up to ModsMaxGB (-1 = no limit).
	// SyncDeployedMods (experimental) also offers the mods deployed in a
	// game's own folder. ShareVortexMods (experimental) shares Vortex's
	// mod list (which mods are installed and enabled, with their details)
	// between PCs, so Vortex manages the same synced mods on each of them.
	FindMods         bool `json:"findMods"`
	AutoAddMods      bool `json:"autoAddMods"`
	SyncDeployedMods bool `json:"syncDeployedMods"`
	ShareVortexMods  bool `json:"shareVortexMods"`
	ModsMaxGB        int  `json:"modsMaxGB"`
	// ModsChanged is when the options above last changed, here or on
	// another PC: they are the same on every PC, and the newest change wins
	// (see meta.ModSettings).
	ModsChanged time.Time `json:"modsChanged,omitzero"`
	// Mods are the synced (or backup-only) folders that are mod folders, by
	// folder id: how they are described to other PCs.
	Mods map[string]ModFolder `json:"mods,omitempty"`
	// Accounts shows accounts (separate saves per person) on this PC. It
	// can only be turned off while no game is split per account.
	Accounts bool `json:"accounts,omitempty"`
}

// ModFolder is a mod manager's folder that Syncer syncs.
type ModFolder struct {
	Kind     string `json:"kind"`    // mods.Kind*
	Manager  string `json:"manager"` // "vortex"
	Game     string `json:"game"`    // the manager's game id
	GameName string `json:"gameName"`
	Root     string `json:"root"` // portable mod root, e.g. "vortex:skyrimse"
	Rel      string `json:"rel"`
	// Role of this PC for deployed mods: "source" (its deployment is sent
	// to the other PCs) or "receiver".
	Role string `json:"role,omitempty"`
	// SizeGB is the size last published (Syncthing can't tell while paused).
	SizeGB int `json:"sizeGB,omitempty"`
}

// Paused reports whether syncing and automatic backups are paused right now.
func (s Settings) Paused() bool { return time.Now().Before(s.PausedUntil) }

// LocalFolder is a save folder known only to this PC.
type LocalFolder struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
	// SyncID is the shared id it had while synced, so syncing it again
	// rejoins the same folder on the other PCs.
	SyncID string `json:"syncID,omitempty"`
	// CopiedFrom is the backup its history was copied from when it was added
	// from "Other backups", so that backup isn't offered again.
	CopiedFrom string `json:"copiedFrom,omitempty"`
}

// BackupRun is the outcome of the last backup.
type BackupRun struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	OK       bool      `json:"ok"`
	Folders  int       `json:"folders"`
	Copied   int       `json:"copied"`
	Versions int       `json:"versions"`
	Bytes    int64     `json:"bytes"`
	Errors   []string  `json:"errors"`
	// Held: files left as they are in the backup because another PC backed
	// up newer saves there (see backup.mirror), per game.
	Held   []string `json:"held,omitempty"`
	Target string   `json:"target"`
	Backed []string `json:"-"` // ids of the folders backed up without errors
}

// State is machine-written status.
type State struct {
	LastBackup  *BackupRun `json:"lastBackup"`
	LastSuccess time.Time  `json:"lastSuccess,omitzero"` // last backup that finished without errors
	// Protected: when the existing files of a folder (id|path) were saved as
	// a restore point before it started syncing.
	Protected map[string]time.Time `json:"protected,omitempty"`
	// PausedFolders are the Syncthing folders a pause stopped, so resuming
	// leaves folders paused by hand alone.
	PausedFolders []string `json:"pausedFolders,omitempty"`
	// GamePaused are the Syncthing folders paused while a game runs, so they
	// resume when it exits (or when Syncer next starts, if it quit first).
	GamePaused []string `json:"gamePaused,omitempty"`
	// GameHeld is when the window last confirmed it holds them.
	GameHeld time.Time `json:"gameHeld,omitzero"`
	// GoogleSynced is when the backup was last synced with Google Drive
	// through Syncer's own sign-in; GoogleError why the last try failed.
	GoogleSynced time.Time `json:"googleSynced,omitzero"`
	GoogleError  string    `json:"googleError,omitempty"`
	Update       *Update   `json:"update,omitempty"` // newest release seen
	// Notified remembers which problems were already reported (key -> when).
	Notified map[string]time.Time `json:"notified,omitempty"`
	// FolderBackups is when each folder (by id) was last backed up without errors.
	FolderBackups map[string]time.Time `json:"folderBackups,omitempty"`
	// BackgroundTask describes the background task as last registered, so it
	// is only registered again when something about it changed.
	BackgroundTask string `json:"backgroundTask,omitempty"`
	// ModHeld are mod folders paused while their mod manager is open, to be
	// resumed when it closes.
	ModHeld []string `json:"modHeld,omitempty"`
	// ModSync is the state of each deployed-mods folder (by id).
	ModSync map[string]ModSyncState `json:"modSync,omitempty"`
	// Decisions are the recent choices between two versions of a save,
	// newest last, so each can be changed later.
	Decisions []Decision `json:"decisions,omitempty"`
}

// Decision is a choice between two versions of a save file.
type Decision struct {
	Folder string    `json:"folder"`
	Rel    string    `json:"rel"` // the save file, relative to the folder
	At     time.Time `json:"at"`
	Kept   string    `json:"kept,omitempty"`  // the PC the version kept was from, if known
	Other  string    `json:"other,omitempty"` // … and the version put aside
	Put    string    `json:"put"`             // where the version put aside is
	// OtherMod is the modification time of the version put aside: that
	// exact version is never taken back over the choice (from another PC's
	// backup, or held in the backup over the version kept).
	OtherMod time.Time `json:"otherMod,omitzero"`
}

// Rejected is a version of a save file that was decided against.
type Rejected struct {
	Rel string `json:"rel"` // forward slashes
	Mod int64  `json:"mod"` // its modification time, unix seconds
}

// RejectedIn lists the versions of a folder's files decided against here.
func (st State) RejectedIn(id string) []Rejected {
	var out []Rejected
	for _, d := range st.Decisions {
		if d.Folder == id && !d.OtherMod.IsZero() {
			out = append(out, Rejected{Rel: filepath.ToSlash(d.Rel), Mod: d.OtherMod.Unix()})
		}
	}
	return out
}

// IsRejected reports whether the file rel, modified at mod, is one of
// rejected (the same file, within two seconds: file systems differ).
func IsRejected(rejected []Rejected, rel string, mod time.Time) bool {
	rel = strings.ToLower(filepath.ToSlash(rel))
	for _, r := range rejected {
		if strings.ToLower(r.Rel) == rel && abs64(mod.Unix()-r.Mod) <= 2 {
			return true
		}
	}
	return false
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// ModSyncState tracks updates of a deployed-mods folder.
type ModSyncState struct {
	Phase      string    `json:"phase"`                // idle | pending | applying | held
	AppliedGen int64     `json:"appliedGen"`           // source inventory generation applied here
	SourceGen  int64     `json:"sourceGen,omitempty"`  // source: generation published
	SourceHash string    `json:"sourceHash,omitempty"` // source: what the published inventory was built from
	Since      int64     `json:"since,omitempty"`      // source: when this PC became the source (unix ns)
	Held       string    `json:"held,omitempty"`       // why it is held
	HeldBy     string    `json:"heldBy,omitempty"`     // what held it: off, unreadable, handover, check, interrupted
	LastAudit  time.Time `json:"lastAudit,omitzero"`
	Started    time.Time `json:"started,omitzero"` // when the update being applied started
	PID        int       `json:"pid,omitempty"`    // the process applying it
	// Source is the PC a receiver takes updates from (its folder is shared
	// with that PC only); Handover is another PC that claims to send them
	// now, waiting for the user to agree.
	Source   string `json:"source,omitempty"`
	Handover string `json:"handover,omitempty"`
}

// Update is the newest Syncer release found on GitHub.
type Update struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"` // tag, e.g. "v0.7.0"
	URL     string    `json:"url"`    // release page
}

var mu sync.Mutex

func defaults() Settings {
	return Settings{Theme: "system", BackupEnabled: true, IntervalHours: 3, KeepDays: 30, AutoAdd: true, AutoAddMaxGB: 1,
		PauseWhileGaming: true, Notify: true, NoBackup: map[string]bool{}, Ignored: map[string]bool{}, Dismissed: map[string]bool{},
		BackupOnly: map[string]LocalFolder{}, Exclude: map[string][]string{}, ModsMaxGB: 20, Mods: map[string]ModFolder{}}
}

// LoadSettings reads settings, falling back to defaults.
func LoadSettings() Settings {
	s := defaults()
	load("settings.json", &s)
	if s.NoBackup == nil {
		s.NoBackup = map[string]bool{}
	}
	if s.Ignored == nil {
		s.Ignored = map[string]bool{}
	}
	if s.Dismissed == nil {
		s.Dismissed = map[string]bool{}
	}
	if s.BackupOnly == nil {
		s.BackupOnly = map[string]LocalFolder{}
	}
	if s.Exclude == nil {
		s.Exclude = map[string][]string{}
	}
	if s.Mods == nil {
		s.Mods = map[string]ModFolder{}
	}
	if s.ModsMaxGB == 0 || s.ModsMaxGB < -1 {
		s.ModsMaxGB = 20
	}
	if !s.FindMods {
		s.AutoAddMods, s.SyncDeployedMods, s.ShareVortexMods = false, false, false
	}
	if s.IntervalHours <= 0 {
		s.IntervalHours = 3
	}
	if s.KeepDays <= 0 {
		s.KeepDays = 30
	}
	if s.AutoAddMaxGB == 0 || s.AutoAddMaxGB < -1 {
		s.AutoAddMaxGB = 1
	}
	return s
}

// SaveSettings writes settings atomically.
func SaveSettings(s Settings) error { return save("settings.json", s) }

// UpdateSettings applies fn to the stored settings under a lock and saves.
func UpdateSettings(fn func(*Settings)) (Settings, error) {
	mu.Lock()
	defer mu.Unlock()
	defer fileLock("settings")()
	s := LoadSettings()
	fn(&s)
	return s, save("settings.json", s)
}

// LoadState reads run state.
func LoadState() State {
	var st State
	load("state.json", &st)
	return st
}

// SaveState writes run state atomically.
func SaveState(st State) error { return save("state.json", st) }

var stateMu sync.Mutex

// UpdateState applies fn to the stored state under a lock and saves.
func UpdateState(fn func(*State)) {
	stateMu.Lock()
	defer stateMu.Unlock()
	defer fileLock("state")()
	st := LoadState()
	fn(&st)
	_ = SaveState(st)
}

// FileLock takes the named cross-process lock (see fileLock).
func FileLock(name string) (unlock func()) { return fileLock(name) }

// fileLock also keeps the other Syncer process (the window and the background
// task run separately) from updating the same file at the same time, which
// would lose one of the two changes. The OS drops the lock if a process dies.
func fileLock(name string) (unlock func()) {
	p, err := windows.UTF16PtrFromString(filepath.Join(paths.AppDir(), name+".lock"))
	if err != nil {
		return func() {}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return func() {} // better an unlocked update than none
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		windows.CloseHandle(h)
		return func() {}
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		windows.CloseHandle(h)
	}
}

func load(name string, v any) {
	b, err := os.ReadFile(filepath.Join(paths.AppDir(), name))
	if err == nil {
		_ = json.Unmarshal(b, v)
	}
}

func save(name string, v any) error {
	return WriteJSON(filepath.Join(paths.AppDir(), name), v)
}

// WriteJSON writes v to path via a uniquely named temp file + rename, so
// readers never see a half-written file and the window and the background
// task never clobber each other's temp file.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
