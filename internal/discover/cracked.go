package discover

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/windows/registry"
)

// A cracked or portable copy of a Steam game has no Steam manifest and often
// no uninstall entry, so it looks uninstalled. Two things still give it away:
// Windows remembers the programs it ran (Game Bar's list of games, the
// Program Compatibility Assistant, the shell's MuiCache), and a Steam
// emulator next to the game's exe names the game's Steam app id
// (steam_appid.txt, AppId= in steam_emu.ini and similar). A remembered exe
// that still exists with such a file beside it is an installed game.

// emuIDFiles hold a Steam app id, in the folder or one level below.
var emuIDFiles = []string{
	"steam_appid.txt", "steam_emu.ini", "ColdClientLoader.ini", "OnlineFix.ini",
	"codex.ini", "rune.ini", "tenoke.ini", "cream_api.ini",
	filepath.Join("steam_settings", "steam_appid.txt"),
}

// appIDLine matches "AppId=123" and OnlineFix's "RealAppId=123" (its plain
// AppId is Spacewar's 480, a stand-in every such copy shares).
var appIDLine = regexp.MustCompile(`(?i)^\s*(real)?app_?id\s*=\s*(\d+)`)

const (
	spacewar      = 480 // Valve's test app, used as a stand-in id
	crackedLevels = 3   // folders above the exe searched for an id file
)

// appIDIn reads the Steam app id an emulator file names (0 if none).
func appIDIn(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	id, real := 0, 0
	sc := bufio.NewScanner(f)
	for n := 0; sc.Scan() && n < 500; n++ {
		line := strings.TrimSpace(sc.Text())
		if strings.EqualFold(filepath.Ext(p), ".txt") {
			id, _ = strconv.Atoi(line)
			break
		}
		if m := appIDLine.FindStringSubmatch(line); m != nil {
			v, _ := strconv.Atoi(m[2])
			if m[1] != "" {
				real = v
			} else if id == 0 {
				id = v
			}
		}
	}
	if real > 0 {
		id = real
	}
	if id == spacewar {
		return 0
	}
	return id
}

// appIDNear looks for an emulator's app id file in dir and up to levels
// folders above it, never at a drive's root. It returns the app id and the
// folder the file is in.
func appIDNear(dir string, levels int) (int, string) {
	dir = filepath.Clean(dir)
	for i := 0; i <= levels; i++ {
		if filepath.Dir(dir) == dir {
			break
		}
		for _, n := range emuIDFiles {
			if id := appIDIn(filepath.Join(dir, n)); id > 0 {
				return id, dir
			}
		}
		dir = filepath.Dir(dir)
	}
	return 0, ""
}

// crackedGame is a game found from an exe or install folder.
type crackedGame struct {
	appID int
	dir   string
}

var crackedCache struct {
	sync.Mutex
	m map[string]crackedGame // exe or folder path (lower case) -> what was found
}

// findCracked looks up the game an exe (or install folder) belongs to,
// cached per path for the life of the process: the answer only changes when
// the game is removed, which the existence check catches.
func findCracked(p string, isExe bool) (crackedGame, bool) {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() == isExe {
		return crackedGame{}, false
	}
	key := strings.ToLower(filepath.Clean(p))
	crackedCache.Lock()
	g, ok := crackedCache.m[key]
	crackedCache.Unlock()
	if !ok {
		if isExe {
			g.appID, g.dir = appIDNear(filepath.Dir(p), crackedLevels)
		} else if g.appID, _ = appIDNear(p, 0); g.appID == 0 {
			// Installers put the exe (and the emulator) in a subfolder: bin\,
			// Game\Bin\, Binaries\Win64\.
			g.appID, _ = appIDBelow(p, 3)
		}
		if g.appID > 0 && !isExe {
			g.dir = p // the whole install folder
		}
		crackedCache.Lock()
		if crackedCache.m == nil {
			crackedCache.m = map[string]crackedGame{}
		}
		crackedCache.m[key] = g
		crackedCache.Unlock()
	}
	return g, g.appID > 0
}

// appIDBelow looks for an emulator's app id file in the folders below dir,
// down to depth levels, giving up in big folders.
func appIDBelow(dir string, depth int) (int, string) {
	type level struct {
		dir   string
		depth int
	}
	queue := []level{{dir, 0}}
	for seen := 0; len(queue) > 0 && seen < 150; seen++ {
		l := queue[0]
		queue = queue[1:]
		if l.depth > 0 {
			for _, n := range emuIDFiles {
				if id := appIDIn(filepath.Join(l.dir, n)); id > 0 {
					return id, l.dir
				}
			}
		}
		if l.depth == depth {
			continue
		}
		es, _ := os.ReadDir(l.dir)
		for _, e := range es {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				queue = append(queue, level{filepath.Join(l.dir, e.Name()), l.depth + 1})
			}
		}
	}
	return 0, ""
}

// loadCracked adds games found from remembered exes and from install
// folders (uninstall entries without a name that matched).
func (i *Installed) loadCracked(exes, dirs []string) {
	add := func(g crackedGame) {
		i.steamIDs[g.appID] = true
		i.addRoot(g.dir)
	}
	for _, exe := range exes {
		// Installers and uninstallers sit next to a game's files too, in
		// Downloads or the install folder; they don't mean it's installed.
		if base := strings.ToLower(filepath.Base(exe)); strings.HasPrefix(base, "setup") || strings.HasPrefix(base, "unins") {
			continue
		}
		if g, ok := findCracked(exe, true); ok {
			add(g)
		}
	}
	for _, d := range dirs {
		if g, ok := findCracked(d, false); ok {
			add(g)
		}
	}
}

// exeTraces lists exes Windows remembers running for this user.
func exeTraces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if i := strings.Index(strings.ToLower(p), ".exe"); i > 0 {
			p = p[:i+4]
		} else {
			return
		}
		if !filepath.IsAbs(p) || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	// Game Bar: the games Windows recognised.
	registryGames(registry.CURRENT_USER, `System\GameConfigStore\Children`, func(k registry.Key) {
		add(registryString(k, "MatchedExeFullPath"))
	})
	// Value names are exe paths ("C:\…\game.exe" or "C:\…\game.exe.FriendlyAppName").
	for _, key := range []string{
		`Software\Microsoft\Windows NT\CurrentVersion\AppCompatFlags\Compatibility Assistant\Store`,
		`Software\Classes\Local Settings\Software\Microsoft\Windows\Shell\MuiCache`,
	} {
		k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(-1)
		k.Close()
		for _, n := range names {
			add(n)
		}
	}
	return out
}
