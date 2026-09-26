package discover

import (
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/steam"
)

// Class is what Syncer knows about a save folder beyond its path, for the
// games list and for folders other PCs sync.
type Class struct {
	// SteamCloud: Steam Cloud keeps this folder on this PC. Reason says why a
	// game that supports Steam Cloud isn't left to it (a steam.Reason*, or
	// "emulator:<group>").
	SteamCloud bool
	Reason     string
	// CopyOf names the game whose own saves this Steam emulator folder copies.
	CopyOf string
	// OneDriveCopy is another copy of the folder on the other side of
	// OneDrive; OneDriveCopyNewer: its newest save is newer than this one's.
	OneDriveCopy      string
	OneDriveCopyNewer bool
}

// cloudVerdict decides whether Steam Cloud keeps dir, a save folder of e, on
// this PC. steam.Check goes by what Steam's own files say; this adds what
// only the rest of the PC tells: a game Steam didn't install here, run from a
// Steam emulator or installed through another store or a crack, never has
// its saves synced by Steam here, whatever an old steam_autocloud.vdf says.
func cloudVerdict(e Entry, dir string, sc *steam.Cloud, emuGroup string, inst *Installed) (covered bool, reason string) {
	if !e.SteamCloud {
		return false, ""
	}
	v := sc.Check(e.SteamID, dir)
	if !sc.SteamInstalled(e.SteamID) {
		switch {
		case emuGroup != "":
			return false, "emulator:" + emuGroup
		case v.Covered && inst.Has(e.Name):
			return false, steam.ReasonNotInstalled
		}
	}
	return v.Covered, v.Reason
}

// stopAt bounds the search for a steam_autocloud.vdf above a save folder:
// never into a known folder or one that holds many games.
func stopAt(broad map[string]bool) func(string) bool {
	return func(d string) bool {
		if broad[strings.ToLower(filepath.Clean(d))] {
			return true
		}
		_, _, ok := paths.Portable(d)
		return !ok
	}
}

// newerCopy reports whether a folder's copy on the other side of OneDrive
// has saves newer than mod, the folder's own newest file.
func newerCopy(twin string, mod time.Time) bool {
	_, _, tm := measure(twin)
	return tm.After(mod.Add(2 * time.Minute))
}

const classifyTTL = 5 * time.Minute

var classCache struct {
	sync.Mutex
	m   map[string]classEntry
	idx *manifestIndex
}

type classEntry struct {
	c  Class
	at time.Time
}

// manifestIndex looks up manifest entries by name and Steam app id.
type manifestIndex struct {
	src    []Entry
	byName map[string]Entry
	byApp  map[int][]Entry
}

func indexFor(es []Entry) *manifestIndex {
	if i := classCache.idx; i != nil && len(i.src) == len(es) && (len(es) == 0 || &i.src[0] == &es[0]) {
		return i
	}
	i := &manifestIndex{src: es, byName: map[string]Entry{}, byApp: map[int][]Entry{}}
	for _, e := range es {
		if _, ok := i.byName[strings.ToLower(e.Name)]; !ok {
			i.byName[strings.ToLower(e.Name)] = e
		}
		if e.SteamID > 0 {
			i.byApp[e.SteamID] = append(i.byApp[e.SteamID], e)
		}
	}
	classCache.idx = i
	return i
}

// Classify describes the save folder at path, known under label (a game
// name from the database, or "Game (RUNE saves)"). It works from the cached
// game database and never downloads it; results are cached for a while,
// since the games list and every reconcile ask about every folder.
func Classify(label, path string) Class {
	key := strings.ToLower(label) + "|" + strings.ToLower(filepath.Clean(path))
	classCache.Lock()
	if e, ok := classCache.m[key]; ok && time.Since(e.at) < classifyTTL {
		classCache.Unlock()
		return e.c
	}
	idx := indexFor(CachedManifest())
	classCache.Unlock()

	c := classify(idx, label, path)

	classCache.Lock()
	if classCache.m == nil {
		classCache.m = map[string]classEntry{}
	}
	for k, e := range classCache.m {
		if time.Since(e.at) >= classifyTTL {
			delete(classCache.m, k)
		}
	}
	classCache.m[key] = classEntry{c, time.Now()}
	classCache.Unlock()
	return c
}

func classify(idx *manifestIndex, label, path string) Class {
	var c Class
	if twin := paths.OneDriveTwin(path); twin != "" {
		_, _, mod := measure(path)
		c.OneDriveCopy, c.OneDriveCopyNewer = twin, newerCopy(twin, mod)
	}
	if app, _, ok := emulatorApp(path); ok {
		var own []string
		ex := newExistCache()
		userName := currentUserName()
		broad := tooBroad()
		for _, e := range idx.byApp[app] {
			for _, raw := range e.Paths {
				for _, d := range resolve(raw, userName, ex) {
					if !broad[strings.ToLower(d)] {
						own = append(own, d)
					}
				}
			}
		}
		if len(own) > 0 && mirrorOf(path, own) {
			c.CopyOf = idx.byApp[app][0].Name
		}
		return c
	}
	e, ok := idx.byName[strings.ToLower(strings.TrimSpace(label))]
	if !ok || !e.SteamCloud {
		return c
	}
	broad := tooBroad()
	sc := steam.Cached(classifyTTL, stopAt(broad))
	group := ""
	for _, s := range emulatorSaves(emulatorDirs()) {
		if s.appID == e.SteamID {
			group = s.group
			break
		}
	}
	c.SteamCloud, c.Reason = cloudVerdict(e, path, sc, group, CachedInstalled(time.Minute))
	return c
}

func currentUserName() string {
	if u, err := user.Current(); err == nil {
		return filepath.Base(u.Username)
	}
	return ""
}
