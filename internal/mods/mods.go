// Package mods finds the folders mod managers keep on this PC (Vortex for
// now), so they can be synced between PCs next to the save folders.
//
// A mod folder is described to other PCs by a root that only this PC can
// resolve ("vortex:<game>" plus "staging" or "profiles", or "game:<game>"
// plus a path inside the game's install folder), never by an absolute path:
// each PC looks up where its own mod manager keeps that game's mods. A
// peer's metadata is untrusted, so nothing it sends can point Syncer at a
// folder this PC's mod manager doesn't use.
package mods

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kinds of mod folder.
const (
	KindStaging  = "mods"          // the mod manager's copy of every installed mod
	KindProfiles = "mods-profiles" // mod lists and load orders
	KindDeployed = "mods-deployed" // mods as deployed into the game's folder (experimental)
)

const (
	relStaging  = "staging"
	relProfiles = "profiles"
	rootGame    = "game:"
)

var (
	// ErrGameMissing: the mod manager doesn't manage that game on this PC.
	ErrGameMissing = errors.New("the mod manager doesn't manage that game on this PC")
	// ErrUnsafe: the root or path must never be synced.
	ErrUnsafe = errors.New("not a mod folder that can be synced")
)

// Found is a mod folder on this PC.
type Found struct {
	Manager  string `json:"manager"`  // "vortex"
	Game     string `json:"game"`     // the manager's id of the game, e.g. "skyrimse"
	GameName string `json:"gameName"` // e.g. "Skyrim Special Edition"
	Kind     string `json:"kind"`
	Root     string `json:"root"` // portable: "vortex:skyrimse"
	Rel      string `json:"rel"`  // portable: "staging"
	Path     string `json:"path"` // where it is on this PC
	// GameDir is the game's install folder (deployed mods only).
	GameDir string   `json:"gameDir,omitempty"`
	Warn    []string `json:"warn,omitempty"`
	// Deploy is the deployment manifest a deployed folder was found by.
	Deploy *Manifest `json:"-"`
}

// Key identifies a mod folder the same way on every PC.
func (f Found) Key() string { return f.Root + "/" + f.Rel }

// Label names the folder in Syncer's lists.
func (f Found) Label() string {
	name := f.GameName
	if name == "" {
		name = f.Game
	}
	switch f.Kind {
	case KindProfiles:
		return name + " (Vortex load order)"
	case KindDeployed:
		return name + " (deployed mods)"
	}
	return name + " (Vortex mods)"
}

// SplitKey splits a Key into its root and rel.
func SplitKey(key string) (root, rel string, ok bool) {
	i := strings.Index(key, "/")
	if i <= 0 || !IsModRoot(key[:i]) {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// Detector finds one mod manager's folders.
type Detector interface {
	Name() string
	// Owns reports whether root is one of this manager's portable roots.
	Owns(root string) bool
	// Detect lists the manager's mod folders on this PC.
	Detect() []Found
	// Resolve finds a portable mod folder on this PC: ErrGameMissing when
	// the manager doesn't manage that game here, ErrUnsafe for a root or
	// rel that must never be synced.
	Resolve(root, rel string) (string, error)
	// Running reports whether the manager is open (procs are executable
	// paths or names of running processes).
	Running(procs []string) bool
	// Ignores are .stignore lines every folder of kind needs.
	Ignores(kind string) []string
}

// Detectors are the supported mod managers.
var Detectors = []Detector{Vortex{}}

var detectCache struct {
	sync.Mutex
	at  time.Time
	out []Found
}

// Detect lists every supported manager's mod folders on this PC. Results
// are reused for a minute: listing walks game folders.
func Detect() []Found {
	detectCache.Lock()
	defer detectCache.Unlock()
	if detectCache.out != nil && time.Since(detectCache.at) < time.Minute {
		return append([]Found(nil), detectCache.out...)
	}
	var out []Found
	for _, d := range Detectors {
		out = append(out, d.Detect()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GameName != out[j].GameName {
			return strings.ToLower(out[i].GameName) < strings.ToLower(out[j].GameName)
		}
		return out[i].Kind < out[j].Kind
	})
	detectCache.out, detectCache.at = out, time.Now()
	return append([]Found(nil), out...)
}

// Forget drops cached detection results, e.g. after a folder was added.
func Forget() {
	detectCache.Lock()
	detectCache.out = nil
	detectCache.Unlock()
	scanCache.Lock()
	scanCache.vs = nil
	scanCache.Unlock()
}

// NameOf is a game's name, from its mod manager id.
func NameOf(game string) string { return gameName(game, "") }

// Find returns the mod folder on this PC with the given Key.
func Find(key string) (Found, bool) {
	for _, f := range Detect() {
		if f.Key() == key {
			return f, true
		}
	}
	return Found{}, false
}

// IsModRoot reports whether a portable root is a mod root rather than one
// of the save roots in package paths (those never contain a colon).
func IsModRoot(root string) bool { return strings.Contains(root, ":") }

// KindOf says which kind of folder a portable mod root and rel describe.
func KindOf(root, rel string) (string, bool) {
	for _, d := range Detectors {
		if !d.Owns(root) {
			continue
		}
		switch {
		case strings.HasPrefix(root, rootGame):
			return KindDeployed, rel != ""
		case rel == relStaging:
			return KindStaging, true
		case rel == relProfiles:
			return KindProfiles, true
		}
	}
	return "", false
}

// Resolve finds a portable mod folder on this PC.
func Resolve(root, rel string) (string, error) {
	for _, d := range Detectors {
		if d.Owns(root) {
			return d.Resolve(root, rel)
		}
	}
	return "", ErrUnsafe
}

// ManagerRunning reports whether any supported mod manager is open.
func ManagerRunning(procs []string) bool {
	for _, d := range Detectors {
		if d.Running(procs) {
			return true
		}
	}
	return false
}

// Ignores are the .stignore lines a folder of kind needs.
func Ignores(kind string) []string {
	var out []string
	for _, d := range Detectors {
		out = append(out, d.Ignores(kind)...)
	}
	return out
}

// Measure sums a folder's files (not following links).
func Measure(dir string) (size int64, files int, mod time.Time) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if files >= 1_000_000 {
			return filepath.SkipAll
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			if info.ModTime().After(mod) {
				mod = info.ModTime()
			}
		}
		files++
		return nil
	})
	return
}
