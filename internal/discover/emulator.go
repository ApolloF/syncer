package discover

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/ApolloF/syncer/internal/paths"
)

// Cracked copies of Steam games run on a Steam emulator, which keeps what
// Steam would put in Steam Cloud in a folder of its own: one subfolder per
// Steam app id. The game database doesn't know these folders, so without
// this a cracked game's saves would go unnoticed.
type emuRoot struct {
	root, rel string
	group     string // "" = every subfolder is a group (…\Steam\CODEX\<appid>)
}

var emulatorRoots = []emuRoot{
	{paths.Public, "Documents/Steam", ""}, // CODEX, RUNE and similar
	{paths.Roaming, "Goldberg SteamEmu Saves", "Goldberg"},
	{paths.Roaming, "GSE Saves", "Goldberg"},
	{paths.Roaming, "EMPRESS", "EMPRESS"},
	{paths.Public, "Documents/EMPRESS", "EMPRESS"},
	{paths.Roaming, "SmartSteamEmu", "SmartSteamEmu"},
}

// emuSuffix is how an emulator save folder is labelled: "Game (RUNE saves)".
var emuSuffix = regexp.MustCompile(`\s*\([^()]*\bsaves\)$`)

// emuDir is one resolved emulator folder holding per-app subfolders.
type emuDir struct{ path, group string }

// emuSave is an emulator's save folder for one game.
type emuSave struct {
	group string
	appID int
	dir   string
}

// emulatorDirs resolves emulatorRoots on this PC.
func emulatorDirs() []emuDir {
	var out []emuDir
	for _, r := range emulatorRoots {
		base, ok := paths.Resolve(r.root, r.rel)
		if !ok {
			continue
		}
		if r.group != "" {
			out = append(out, emuDir{base, r.group})
			continue
		}
		es, _ := os.ReadDir(base)
		for _, e := range es {
			if e.IsDir() {
				out = append(out, emuDir{filepath.Join(base, e.Name()), e.Name()})
			}
		}
	}
	return out
}

// emulatorSaves lists per-game folders in dirs that hold saves: a remote\
// folder with at least one file in it.
func emulatorSaves(dirs []emuDir) []emuSave {
	var out []emuSave
	for _, d := range dirs {
		es, _ := os.ReadDir(d.path)
		for _, e := range es {
			id, err := strconv.Atoi(e.Name())
			if err != nil || id <= 0 || !e.IsDir() {
				continue
			}
			dir := filepath.Join(d.path, e.Name())
			if hasFiles(filepath.Join(dir, "remote")) {
				out = append(out, emuSave{d.group, id, dir})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out
}

func hasFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
