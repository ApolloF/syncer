package discover

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

// emulatorApp reports whether dir is an emulator's per-game folder
// (…\Steam\RUNE\<appid>) and for which app.
func emulatorApp(dir string) (appID int, group string, ok bool) {
	parent := filepath.Dir(filepath.Clean(dir))
	id, err := strconv.Atoi(filepath.Base(dir))
	if err != nil || id <= 0 {
		return 0, "", false
	}
	for _, d := range emulatorDirs() {
		if strings.EqualFold(filepath.Clean(d.path), parent) {
			return id, d.group, true
		}
	}
	return 0, "", false
}

// A save file name must show up in both places this often before an emulator
// folder counts as a copy of the game's own saves.
const (
	mirrorMin   = 3
	mirrorFiles = 5000
)

// mirrorOf reports whether an emulator folder only holds a copy of saves the
// game also keeps in one of gameDirs. Games using Steam Cloud through its API
// (Baldur's Gate 3) write every save twice: into their own save folder and
// through Steam, which an emulator redirects into its remote\ folder, often
// with the names lowercased. Emulator folders that hold the game's only
// saves have (almost) no names in common with any other folder.
func mirrorOf(emuDir string, gameDirs []string) bool {
	emu := baseNames(filepath.Join(emuDir, "remote"))
	if len(emu) < mirrorMin {
		return false
	}
	for _, d := range gameDirs {
		own := baseNames(d)
		n := 0
		for k := range emu {
			if own[k] {
				n++
			}
		}
		// A quarter of the smaller set: the emulator's copy keeps saves the
		// game has since deleted, and a game folder may hold more than saves.
		if n >= mirrorMin && 4*n >= min(len(emu), len(own)) {
			return true
		}
	}
	return false
}

// baseNames returns the lower-case file names below dir (capped).
func baseNames(dir string) map[string]bool {
	out := map[string]bool{}
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if len(out) >= mirrorFiles {
			return filepath.SkipAll
		}
		if n := strings.ToLower(d.Name()); n != "steam_autocloud.vdf" && n != "desktop.ini" {
			out[n] = true
		}
		return nil
	})
	return out
}
