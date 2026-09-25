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
	Migrated          bool            `json:"migrated"`     // legacy script setup adopted
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
	LastBackup *BackupRun `json:"lastBackup"`
}

var mu sync.Mutex

func defaults() Settings {
	return Settings{Theme: "system", BackupEnabled: true, IntervalHours: 3, KeepDays: 30, AutoAdd: true, AutoAddMaxGB: 1,
		NoBackup: map[string]bool{}, Ignored: map[string]bool{}, Dismissed: map[string]bool{}}
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

func load(name string, v any) {
	b, err := os.ReadFile(filepath.Join(paths.AppDir(), name))
	if err == nil {
		_ = json.Unmarshal(b, v)
	}
}

func save(name string, v any) error {
	return WriteJSON(filepath.Join(paths.AppDir(), name), v)
}

// WriteJSON writes v to path via a temp file + rename so readers never see a
// half-written file.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
