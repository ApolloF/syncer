package steam

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// App is a game Steam installed: it has an appmanifest_<id>.acf in a library.
// Copies installed outside Steam (a crack, a repack) have none.
type App struct {
	ID        int
	Name      string
	Dir       string // install folder; "" if the manifest's installdir is unsafe
	Installed bool   // installed and playable through Steam (possibly waiting for an update)
}

// StateFlags bits of an appmanifest (EAppState).
const (
	stateUpdateRequired = 2
	stateFullyInstalled = 4
	stateUpdateRunning  = 256
	stateUpdatePaused   = 512
	stateUpdateStarted  = 1024
	stateUninstalling   = 2048
)

// Libraries returns Steam's own folder plus every library folder listed in
// its libraryfolders.vdf.
func Libraries(root string) []string {
	if !filepath.IsAbs(root) {
		return nil
	}
	libs := []string{filepath.Clean(root)}
	f, err := os.Open(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	defer f.Close()
	seen := map[string]bool{strings.ToLower(libs[0]): true}
	var extra []string
	for _, lib := range ParseVDF(f).Get("libraryfolders").Kids() {
		dir := filepath.Clean(lib.Value("path"))
		if key := strings.ToLower(dir); filepath.IsAbs(dir) && !seen[key] {
			extra = append(extra, dir)
			seen[key] = true
		}
	}
	sort.Strings(extra)
	return append(libs, extra...)
}

// LibraryApps reads the app manifests of one Steam library.
func LibraryApps(lib string) []App {
	files, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
	var out []App
	for _, file := range files {
		st := readVDF(file).Get("AppState")
		id, err := strconv.Atoi(st.Value("appid"))
		if err != nil || id <= 0 {
			continue
		}
		app := App{ID: id, Name: st.Value("name")}
		common := filepath.Join(lib, "steamapps", "common")
		installDir := st.Value("installdir")
		if dir := filepath.Join(common, installDir); filepath.IsLocal(installDir) && !strings.EqualFold(dir, common) && within(common, dir) {
			app.Dir = dir
		}
		flags, _ := strconv.Atoi(st.Value("StateFlags"))
		playable := flags&stateFullyInstalled != 0 ||
			flags&(stateUpdateRequired|stateUpdateRunning|stateUpdatePaused|stateUpdateStarted) != 0
		app.Installed = playable && flags&stateUninstalling == 0 && app.Dir != "" && isDir(app.Dir)
		out = append(out, app)
	}
	return out
}

// Apps returns every game Steam knows as installed, across all libraries.
func Apps(root string) map[int]App {
	m := map[int]App{}
	for _, lib := range Libraries(root) {
		for _, a := range LibraryApps(lib) {
			if old, ok := m[a.ID]; !ok || (!old.Installed && a.Installed) {
				m[a.ID] = a
			}
		}
	}
	return m
}
