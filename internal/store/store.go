// Package store persists Syncer's local settings and run state as JSON.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Settings are user choices local to this PC.
type Settings struct {
	Theme         string          `json:"theme"`         // system | light | dark
	BackupEnabled bool            `json:"backupEnabled"` // scheduled Drive backup on/off
	BackupRoot    string          `json:"backupRoot"`    // override for the Drive folder; "" = auto-detect
	DriveRoot     string          `json:"driveRoot"`     // chosen "My Drive" when several accounts are signed in; "" = first found
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
	Target   string    `json:"target"`
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
	Update        *Update  `json:"update,omitempty"` // newest release seen
	// Notified remembers which problems were already reported (key -> when).
	Notified map[string]time.Time `json:"notified,omitempty"`
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
		BackupOnly: map[string]LocalFolder{}}
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
	st := LoadState()
	fn(&st)
	_ = SaveState(st)
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
