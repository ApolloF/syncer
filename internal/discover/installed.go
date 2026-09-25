package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/steam"
	"golang.org/x/sys/windows/registry"
)

// Installed is a snapshot of locally installed names, Steam IDs and game folders.
type Installed struct {
	names    map[string]bool
	dirNames map[string]bool
	steamIDs map[int]bool
	roots    []string
	once     sync.Once
	entries  map[string]Entry
}

// LoadInstalled reads local store metadata and uninstall names, skipping errors.
func LoadInstalled() *Installed {
	i := &Installed{
		names: make(map[string]bool), dirNames: make(map[string]bool),
		steamIDs: make(map[int]bool),
	}
	i.loadSteam()
	i.loadEpic()
	registryGames(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\GOG.com\Games`, func(k registry.Key) {
		i.add(registryString(k, "gameName"), registryString(k, "path"))
	})
	registryGames(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Ubisoft\Launcher\Installs`, func(k registry.Key) {
		dir := registryString(k, "InstallDir")
		i.add(filepath.Base(filepath.Clean(dir)), dir)
	})
	i.loadXbox()
	for _, source := range []struct {
		key  registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	} {
		registryGames(source.key, source.path, func(k registry.Key) {
			i.add(registryString(k, "DisplayName"), "")
		})
	}
	return i
}

var installedCache struct {
	sync.Mutex
	inst *Installed
	at   time.Time
}

// CachedInstalled returns a snapshot of installed games at most maxAge old.
// Loading one reads the registry and every store's files, too much for each
// refresh of the window or each reconcile of the shared folder list.
func CachedInstalled(maxAge time.Duration) *Installed {
	installedCache.Lock()
	defer installedCache.Unlock()
	if installedCache.inst == nil || time.Since(installedCache.at) > maxAge {
		installedCache.inst, installedCache.at = LoadInstalled(), time.Now()
	}
	return installedCache.inst
}

// Has reports whether label identifies an installed game or uninstall name.
// An emulator save folder ("Game (RUNE saves)") counts as its game.
func (i *Installed) Has(label string) bool {
	label = strings.TrimSpace(emuSuffix.ReplaceAllString(label, ""))
	if i == nil || len(normalize(label)) < 3 {
		return false
	}
	if i.matches(label, nil) {
		return true
	}
	i.once.Do(func() {
		i.entries = make(map[string]Entry)
		for _, e := range CachedManifest() {
			i.entries[strings.ToLower(e.Name)] = e
		}
	})
	return i.matches(label, i.entries)
}

func (i *Installed) matches(label string, entries map[string]Entry) bool {
	n := normalize(label)
	if len(n) < 3 {
		return false
	}
	if i.names[n] || i.dirNames[n] {
		return true
	}
	e, ok := entries[strings.ToLower(label)]
	if !ok {
		return false
	}
	if e.SteamID > 0 && i.steamIDs[e.SteamID] {
		return true
	}
	for _, dir := range e.InstallDirs {
		if i.dirNames[normalize(dir)] {
			return true
		}
	}
	return false
}

// GameDirs returns absolute store install folders, excluding uninstall entries.
func (i *Installed) GameDirs() []string {
	if i == nil {
		return nil
	}
	return append([]string(nil), i.roots...)
}

// Running reports whether a process executable lies in a store install folder.
func (i *Installed) Running(procPaths []string) bool {
	if i == nil {
		return false
	}
	for _, exe := range procPaths {
		if !filepath.IsAbs(exe) {
			continue
		}
		for _, dir := range i.roots {
			if paths.Within(dir, exe) {
				return true
			}
		}
	}
	return false
}

// normalize keeps only letters and digits (any script), lowercased, so
// "Game™: Edition" and "game edition" compare equal. Minimum-length checks
// use bytes, which lets short non-Latin titles (3 bytes per CJK rune) count.
func normalize(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (i *Installed) add(name, dir string) {
	if n := normalize(name); n != "" {
		i.names[n] = true
	}
	if !filepath.IsAbs(dir) {
		return
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return
	}
	if n := normalize(filepath.Base(dir)); n != "" {
		i.dirNames[n] = true
	}
	for _, root := range i.roots {
		if strings.EqualFold(root, dir) {
			return
		}
	}
	i.roots = append(i.roots, dir)
}

// steamLibraries returns Steam's own folder plus every library folder listed
// in its libraryfolders.vdf.
func steamLibraries(root string) []string { return steam.Libraries(root) }

func (i *Installed) loadSteam() {
	for _, lib := range steamLibraries(steam.Dir()) {
		i.loadSteamLibrary(lib)
	}
}

func (i *Installed) loadSteamLibrary(lib string) {
	for _, app := range steam.LibraryApps(lib) {
		i.steamIDs[app.ID] = true
		i.add(app.Name, app.Dir)
	}
}

func (i *Installed) loadEpic() {
	root := os.Getenv("ProgramData")
	if !filepath.IsAbs(root) {
		return
	}
	files, _ := filepath.Glob(filepath.Join(root, "Epic", "EpicGamesLauncher", "Data", "Manifests", "*.item"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var item struct{ DisplayName, InstallLocation string }
		if err := json.Unmarshal(data, &item); err != nil {
			continue
		}
		i.add(item.DisplayName, item.InstallLocation)
	}
}

func (i *Installed) loadXbox() {
	for drive := 'C'; drive <= 'Z'; drive++ {
		root := string(drive) + `:\`
		if _, err := os.Stat(root); err != nil {
			continue
		}
		dir := filepath.Join(root, "XboxGames")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				i.add(e.Name(), filepath.Join(dir, e.Name()))
			}
		}
	}
}

func registryString(k registry.Key, name string) string {
	s, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return s
}

func registryGames(root registry.Key, path string, visit func(registry.Key)) {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	for _, name := range names {
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		visit(sub)
		sub.Close()
	}
}
