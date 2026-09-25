package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/ApolloF/syncer/internal/paths"
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

// Has reports whether label identifies an installed game or uninstall name.
func (i *Installed) Has(label string) bool {
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

var storePair = regexp.MustCompile(`"([^"\r\n]+)"\s+"((?:\\.|[^"\\])*)"`)

func storePairs(data []byte) map[string]string {
	pairs := make(map[string]string)
	for _, m := range storePair.FindAllSubmatch(data, -1) {
		value := strings.ReplaceAll(string(m[2]), `\\`, `\`)
		pairs[strings.ToLower(string(m[1]))] = value
	}
	return pairs
}

func steamLibraries(root string) []string {
	if !filepath.IsAbs(root) {
		return nil
	}
	libs := []string{filepath.Clean(root)}
	data, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	seen := map[string]bool{strings.ToLower(libs[0]): true}
	for _, m := range storePair.FindAllSubmatch(data, -1) {
		if !strings.EqualFold(string(m[1]), "path") {
			continue
		}
		dir := filepath.Clean(strings.ReplaceAll(string(m[2]), `\\`, `\`))
		key := strings.ToLower(dir)
		if filepath.IsAbs(dir) && !seen[key] {
			libs = append(libs, dir)
			seen[key] = true
		}
	}
	return libs
}

func (i *Installed) loadSteam() {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	root := registryString(k, "SteamPath")
	k.Close()
	for _, lib := range steamLibraries(root) {
		i.loadSteamLibrary(lib)
	}
}

func (i *Installed) loadSteamLibrary(lib string) {
	files, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		p := storePairs(data)
		if id, err := strconv.Atoi(p["appid"]); err == nil && id > 0 {
			i.steamIDs[id] = true
		}
		common := filepath.Join(lib, "steamapps", "common")
		dir := filepath.Join(common, p["installdir"])
		if !filepath.IsLocal(p["installdir"]) || strings.EqualFold(dir, common) || !paths.Within(common, dir) {
			dir = ""
		}
		i.add(p["name"], dir)
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
