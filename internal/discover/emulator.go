package discover

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/fsx"
	"github.com/ApolloF/syncer/internal/paths"
)

// Cracked copies of Steam games run on a Steam emulator, which keeps what
// Steam would put in Steam Cloud in a folder of its own: one subfolder per
// Steam app id. The game database doesn't know these folders, so without
// this a cracked game's saves would go unnoticed. Cracked Ubisoft games do
// the same through a Ubisoft Connect emulator (upc_r2): one subfolder per
// Ubisoft game id, with the save files right in it.
type emuRoot struct {
	root, rel string
	group     string // "" = every subfolder is a group (…\Steam\CODEX\<appid>)
	ubisoft   bool   // subfolders are Ubisoft game ids, not Steam app ids
}

var emulatorRoots = []emuRoot{
	{root: paths.Public, rel: "Documents/Steam"}, // CODEX, RUNE and similar
	{root: paths.Roaming, rel: "Goldberg SteamEmu Saves", group: "Goldberg"},
	{root: paths.Roaming, rel: "GSE Saves", group: "Goldberg"},
	{root: paths.Roaming, rel: "EMPRESS", group: "EMPRESS"},
	{root: paths.Public, rel: "Documents/EMPRESS", group: "EMPRESS"},
	{root: paths.Roaming, rel: "SmartSteamEmu", group: "SmartSteamEmu"},
	{root: paths.Roaming, rel: "Goldberg UplayEmu Saves", group: ubisoftEmuGroup, ubisoft: true},
}

const ubisoftEmuGroup = "UplayEmu"

// emuSuffix is how an emulator save folder is labelled: "Game (RUNE saves)".
var emuSuffix = regexp.MustCompile(`\s*\([^()]*\bsaves\)$`)

// emuDir is one resolved emulator folder holding per-game subfolders.
type emuDir struct {
	path, group string
	ubisoft     bool
}

// emuSave is an emulator's save folder for one game. appID is a Ubisoft
// game id when ubisoft is set.
type emuSave struct {
	group   string
	appID   int
	dir     string
	ubisoft bool
}

// saves is the folder below one of the emulator's per-game folders that
// holds the game's saves.
func (d emuDir) saves(game string) string {
	if d.ubisoft {
		return game
	}
	return filepath.Join(game, "remote")
}

// emulatorDirs resolves emulatorRoots on this PC, plus the save folders
// cracked Ubisoft games set in their emulator's ini.
func emulatorDirs() []emuDir {
	var out []emuDir
	for _, r := range emulatorRoots {
		base, ok := paths.Resolve(r.root, r.rel)
		if !ok {
			continue
		}
		if r.group != "" {
			out = append(out, emuDir{base, r.group, r.ubisoft})
			continue
		}
		es, _ := os.ReadDir(base)
		for _, e := range es {
			if e.IsDir() {
				out = append(out, emuDir{filepath.Join(base, e.Name()), e.Name(), r.ubisoft})
			}
		}
	}
next:
	for _, p := range CachedInstalled(time.Minute).UbisoftSavePaths() {
		for _, d := range out {
			if strings.EqualFold(d.path, p) {
				continue next
			}
		}
		out = append(out, emuDir{p, ubisoftEmuGroup, true})
	}
	return out
}

// emulatorSaves lists per-game folders in dirs that hold saves: any file in
// the folder the emulator keeps them in (remote\ for Steam emulators).
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
			if hasFiles(d.saves(dir)) {
				out = append(out, emuSave{d.group, id, dir, d.ubisoft})
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
// (…\Steam\RUNE\<appid>), for which game, and in which emulator folder.
func emulatorApp(dir string) (appID int, in emuDir, ok bool) {
	parent := filepath.Dir(filepath.Clean(dir))
	id, err := strconv.Atoi(filepath.Base(dir))
	if err != nil || id <= 0 {
		return 0, emuDir{}, false
	}
	for _, d := range emulatorDirs() {
		if strings.EqualFold(filepath.Clean(d.path), parent) {
			return id, d, true
		}
	}
	return 0, emuDir{}, false
}

// Limits for telling whether an emulator folder copies the game's own saves.
const (
	mirrorMin   = 3        // files in common before a bigger folder counts as a copy
	mirrorFiles = 5000     // files read per folder
	hashMin     = 256      // smaller files (flags, empty slots) prove nothing by content
	hashMax     = 64 << 20 // bigger files are only compared by name
	hashBudget  = 512 << 20
)

// mirrorOf reports whether an emulator folder only holds a copy of saves the
// game also keeps in gameDirs. Games using Steam Cloud through its API
// (Baldur's Gate 3) write every save twice: into their own save folder and
// through Steam, which an emulator redirects into its remote\ folder, often
// with the names lowercased or changed. Emulator folders that hold the
// game's only saves have (almost) nothing in common with the game's folders.
func mirrorOf(emuDir string, gameDirs []string) bool {
	return mirrorIn(filepath.Join(emuDir, "remote"), gameDirs)
}

// mirrorIn is mirrorOf for the folder the emulator keeps the saves in.
//
// A file is shared when the game's folders hold one with the same name
// (case-insensitive) or the same bytes. The game's folders count together:
// a game like Hogwarts Legacy may keep saves in more than one. An emulator
// folder with fewer than mirrorMin files only counts as a copy when every
// file in it is in the game's folders byte for byte; a name alone is too
// weak a sign for one or two files.
func mirrorIn(saves string, gameDirs []string) bool {
	emu := listFiles(saves)
	if len(emu) == 0 {
		return false
	}
	own := map[string]fileRef{}
	bySize := map[int64][]string{}
	for _, d := range gameDirs {
		for n, f := range listFiles(d) {
			if _, dup := own[n]; !dup {
				own[n] = f
			}
			if len(bySize[f.size]) < 20 {
				bySize[f.size] = append(bySize[f.size], f.path)
			}
		}
	}
	if len(own) == 0 {
		return false
	}
	budget := int64(hashBudget)
	used := map[string]bool{} // each of the game's files matches one file at most
	sameBytes := func(f fileRef) bool {
		if f.size < hashMin || f.size > hashMax {
			return false
		}
		for _, p := range bySize[f.size] {
			if used[p] {
				continue
			}
			if budget < 2*f.size {
				return false
			}
			budget -= 2 * f.size
			if same, err := fsx.SameContent(f.path, p); err == nil && same {
				used[p] = true
				return true
			}
		}
		return false
	}
	small := len(emu) < mirrorMin
	n := 0
	for name, f := range emu {
		if small {
			if sameBytes(f) {
				n++
			}
		} else if _, named := own[name]; named || sameBytes(f) {
			n++
		}
	}
	if small {
		return n == len(emu)
	}
	// A quarter of the smaller set: the emulator's copy keeps saves the
	// game has since deleted, and a game folder may hold more than saves.
	return n >= mirrorMin && 4*n >= min(len(emu), len(own))
}

// fileRef is one file found by listFiles.
type fileRef struct {
	path string
	size int64
}

// listFiles returns the files below dir by lower-case name (capped; the
// first of several files with one name wins).
func listFiles(dir string) map[string]fileRef {
	out := map[string]fileRef{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if len(out) >= mirrorFiles {
			return filepath.SkipAll
		}
		n := strings.ToLower(d.Name())
		if n == "steam_autocloud.vdf" || n == "desktop.ini" {
			return nil
		}
		if _, dup := out[n]; dup {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			out[n] = fileRef{p, fi.Size()}
		}
		return nil
	})
	return out
}
