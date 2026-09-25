package discover

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/steam"
)

// Found is a game whose save folder exists on this PC.
type Found struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// SteamCloud: Steam Cloud really keeps this save for the Steam account on
	// this PC. SteamCloudUnverified: the game supports Steam Cloud, but that
	// couldn't be confirmed here, so Syncer covers it; SteamCloudReason says
	// why (a steam.Reason*, or "emulator:<group>" for a cracked copy).
	SteamCloud           bool   `json:"steamCloud"`
	SteamCloudUnverified bool   `json:"steamCloudUnverified"`
	SteamCloudReason     string `json:"steamCloudReason"`
	// Emulator names the Steam emulator (e.g. "RUNE") whose save folder this is.
	Emulator string `json:"emulator"`
	// OneDrive: the folder is in OneDrive, which already syncs it between PCs.
	OneDrive bool      `json:"oneDrive"`
	Known    bool      `json:"known"` // false = heuristic, not in the database
	Size     int64     `json:"size"`
	Files    int       `json:"files"`
	Modified time.Time `json:"modified"`
}

var placeholders = map[string]string{
	"<winAppData>":      paths.Roaming,
	"<winLocalAppData>": paths.Local,
	"<winDocuments>":    paths.Documents,
	"<home>":            paths.Home,
	"<winPublic>":       paths.Public,
	"<winProgramData>":  paths.ProgramData,
}

// tooBroad are folders that hold many games; syncing them whole is never right.
func tooBroad() map[string]bool {
	m := map[string]bool{}
	for _, r := range paths.Roots() {
		m[strings.ToLower(r)] = true
	}
	for _, rel := range [][2]string{{paths.Documents, "My Games"}, {paths.Home, "AppData"},
		{paths.Local, "Packages"}, {paths.Local, "Temp"}, {paths.Local, "Programs"}, {paths.Roaming, "Microsoft"},
		{paths.Local, "Microsoft"}, {paths.Home, "OneDrive"}, {paths.Home, "Documents"},
		// Engine-wide containers shared by many games.
		{paths.Roaming, "RenPy"}, {paths.LocalLow, "Unity"}, {paths.Local, "UnrealEngine"},
		{paths.Local, "CrashDumps"}, {paths.Roaming, "Godot"}, {paths.Roaming, "Godot/app_userdata"}} {
		if p, ok := paths.Resolve(rel[0], rel[1]); ok {
			m[strings.ToLower(p)] = true
		}
	}
	return m
}

// Scan resolves every manifest entry against this PC and returns the games
// found, plus unrecognised folders in Saved Games and Documents\My Games.
func Scan(entries []Entry) []Found {
	userName := ""
	if u, err := user.Current(); err == nil {
		userName = filepath.Base(u.Username)
	}
	broad := tooBroad()
	ex := newExistCache()
	sc := steam.Detect()
	emuDirs := emulatorDirs()
	for _, d := range emuDirs {
		broad[strings.ToLower(d.path)] = true
		broad[strings.ToLower(filepath.Dir(d.path))] = true
	}
	emuSaves := emulatorSaves(emuDirs)
	emuByApp := map[int]string{}
	for _, s := range emuSaves {
		if emuByApp[s.appID] == "" {
			emuByApp[s.appID] = s.group
		}
	}

	type hit struct {
		name     string
		dir      string
		cloud    bool   // confirmed Steam Cloud
		unverify bool   // supports Steam Cloud, not confirmed
		reason   string // why not confirmed
	}
	var mu sync.Mutex
	var hits []hit
	work := make(chan Entry, 256)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				for _, raw := range e.Paths {
					for _, d := range resolve(raw, userName, ex) {
						if broad[strings.ToLower(d)] {
							continue
						}
						h := hit{name: e.Name, dir: d}
						if e.SteamCloud {
							v := sc.Check(e.SteamID, d)
							h.cloud, h.unverify, h.reason = v.Covered, !v.Covered, v.Reason
							if g := emuByApp[e.SteamID]; !v.Covered && g != "" {
								h.reason = "emulator:" + g
							}
						}
						mu.Lock()
						hits = append(hits, h)
						mu.Unlock()
					}
				}
			}
		}()
	}
	for _, e := range entries {
		work <- e
	}
	close(work)
	wg.Wait()

	// One entry per directory; drop directories nested inside another hit of
	// the same game (the outer one already covers them).
	byGame := map[string][]hit{}
	for _, h := range hits {
		byGame[h.name] = append(byGame[h.name], h)
	}
	seenDir := map[string]bool{}
	var out []Found
	for name, hs := range byGame {
		sort.Slice(hs, func(i, j int) bool { return len(hs[i].dir) < len(hs[j].dir) })
		var kept []hit
	next:
		for _, h := range hs {
			for _, k := range kept {
				if paths.Within(k.dir, h.dir) {
					continue next
				}
			}
			kept = append(kept, h)
		}
		// Several sibling slots (savegame1, savegame2, ...) become their parent.
		byParent := map[string][]hit{}
		for _, k := range kept {
			byParent[strings.ToLower(filepath.Dir(k.dir))] = append(byParent[strings.ToLower(filepath.Dir(k.dir))], k)
		}
		kept = kept[:0]
		for _, g := range byParent {
			parent := filepath.Dir(g[0].dir)
			if len(g) > 1 && !broad[strings.ToLower(parent)] {
				merged := hit{name: g[0].name, dir: parent, cloud: true}
				for _, k := range g {
					// The parent is only in Steam Cloud if every slot is.
					merged.cloud = merged.cloud && k.cloud
					merged.unverify = merged.unverify || k.unverify
					if merged.reason == "" {
						merged.reason = k.reason
					}
				}
				kept = append(kept, merged)
			} else {
				kept = append(kept, g...)
			}
		}
		for _, k := range kept {
			key := strings.ToLower(k.dir)
			if seenDir[key] {
				continue
			}
			seenDir[key] = true
			out = append(out, Found{Name: name, Path: k.dir, SteamCloud: k.cloud, SteamCloudUnverified: k.unverify,
				SteamCloudReason: k.reason, Known: true})
		}
	}

	// Saves a Steam emulator keeps for a cracked copy: "Game (RUNE saves)".
	names := map[int]string{}
	for _, e := range entries {
		if _, ok := names[e.SteamID]; !ok && e.SteamID > 0 {
			names[e.SteamID] = e.Name
		}
	}
	for _, s := range emuSaves {
		name, ok := names[s.appID]
		key := strings.ToLower(s.dir)
		if !ok || seenDir[key] {
			continue
		}
		seenDir[key] = true
		out = append(out, Found{Name: name + " (" + s.group + " saves)", Path: s.dir, Emulator: s.group, Known: true})
	}

	// Heuristic: anything else in the classic save containers.
	for _, rel := range [][2]string{{paths.SavedGames, ""}, {paths.Documents, "My Games"}} {
		base, ok := paths.Resolve(rel[0], rel[1])
		if !ok {
			continue
		}
		es, _ := os.ReadDir(base)
		for _, e := range es {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			d := filepath.Join(base, e.Name())
			covered := false
			for _, f := range out {
				if paths.Within(d, f.Path) || paths.Within(f.Path, d) {
					covered = true
					break
				}
			}
			if !covered && !strings.EqualFold(e.Name(), "desktop.ini") {
				out = append(out, Found{Name: e.Name(), Path: d})
			}
		}
	}

	var wg2 sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range out {
		wg2.Add(1)
		sem <- struct{}{}
		go func(f *Found) {
			defer wg2.Done()
			f.Size, f.Files, f.Modified = measure(f.Path)
			<-sem
		}(&out[i])
	}
	wg2.Wait()
	od := paths.OneDriveRoots()
	for i := range out {
		out[i].OneDrive = paths.WithinAny(od, out[i].Path)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// resolve expands one manifest path to existing save directories.
func resolve(raw, userName string, ex *existCache) []string {
	var abs string
	for ph, root := range placeholders {
		if strings.HasPrefix(raw, ph) {
			base := paths.Root(root)
			if base == "" {
				return nil
			}
			abs = base + raw[len(ph):]
			break
		}
	}
	if abs == "" {
		return nil
	}
	if strings.Contains(abs, "<") {
		abs = strings.ReplaceAll(abs, "<osUserName>", userName)
		// Per-account subfolders: sync their parent so every account (and a
		// different account id on another PC) is covered.
		if i := strings.Index(abs, "<storeUserId>"); i >= 0 {
			abs = abs[:strings.LastIndexAny(abs[:i], `/\`)]
		}
		if strings.Contains(abs, "<") {
			return nil // install-dir based (<base>, <root>, <game>)
		}
	}
	abs = filepath.Clean(filepath.FromSlash(abs))

	if !strings.ContainsAny(abs, "*?[") {
		fi, ok := ex.stat(abs)
		if !ok {
			return nil
		}
		if fi {
			return []string{abs}
		}
		return []string{filepath.Dir(abs)}
	}
	// Only glob when the fixed prefix exists; this keeps the scan fast.
	fixed := abs[:strings.IndexAny(abs, "*?[")]
	fixed = fixed[:strings.LastIndex(fixed, `\`)+1]
	if isDir, ok := ex.stat(filepath.Clean(fixed)); !ok || !isDir {
		return nil
	}
	ms, _ := filepath.Glob(abs)
	seen := map[string]bool{}
	var out []string
	for _, m := range ms {
		d := m
		if isDir, _ := ex.stat(m); !isDir {
			d = filepath.Dir(m)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// existCache memoises stat results; thousands of manifest paths share parents,
// and a missing parent answers all its children without touching the disk.
type existCache struct {
	mu sync.Mutex
	m  map[string]int8 // 0 unknown, 1 missing, 2 file, 3 dir
}

func newExistCache() *existCache { return &existCache{m: map[string]int8{}} }

// stat returns (isDir, exists).
func (c *existCache) stat(p string) (bool, bool) {
	k := strings.ToLower(p)
	c.mu.Lock()
	v := c.m[k]
	c.mu.Unlock()
	if v == 0 {
		parent := filepath.Dir(p)
		if parent != p {
			if pd, ok := c.stat(parent); !ok || !pd {
				v = 1
			}
		}
		if v == 0 {
			fi, err := os.Stat(p)
			switch {
			case err != nil:
				v = 1
			case fi.IsDir():
				v = 3
			default:
				v = 2
			}
		}
		c.mu.Lock()
		c.m[k] = v
		c.mu.Unlock()
	}
	return v == 3, v >= 2
}

// measure sums size, file count and newest mtime, capped for huge folders.
func measure(dir string) (size int64, files int, mod time.Time) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if files >= 20000 {
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
