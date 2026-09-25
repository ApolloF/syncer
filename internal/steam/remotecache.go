package steam

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// entry is one file Steam Cloud tracks for an app.
type entry struct {
	// rel is the path below its Steam Cloud root ("stardewvalley\saves\farm\farm"),
	// lower case. The root number isn't used to find the file: games can
	// override roots per OS (Cult of the Lamb says "game install" but saves
	// in LocalLow), so a file is matched by this path suffix instead.
	rel       string
	localTime time.Time // the file's modification time when Steam last synced it
}

// readRemoteCache lists the files in an app's remotecache.vdf.
func readRemoteCache(p string) []entry {
	var out []entry
	for _, app := range readVDF(p).Kids() { // one child, named by the app id
		for rel, n := range app.Kids() {
			rel = strings.Trim(strings.ReplaceAll(rel, "/", `\`), `\`)
			if rel == "" {
				continue
			}
			e := entry{rel: strings.ToLower(rel)}
			if t, err := strconv.ParseInt(n.Value("localtime"), 10, 64); err == nil && t > 0 {
				e.localTime = time.Unix(t, 0)
			} else if t, err := strconv.ParseInt(n.Value("time"), 10, 64); err == nil && t > 0 {
				e.localTime = time.Unix(t, 0)
			}
			out = append(out, e)
		}
	}
	return out
}

// modSaveExts are files mods keep next to the game's saves that Steam Cloud
// doesn't sync: script extender co-saves and Seamless Co-op saves.
var modSaveExts = map[string]bool{
	".skse": true, ".f4se": true, ".obse": true, ".nvse": true, ".fose": true, ".sfse": true, ".co2": true,
}

const (
	// A save newer than Steam's record by more than this changed without Steam.
	syncSlack = 2 * time.Minute
	// Changes younger than this are left alone: Steam may still be syncing
	// right after the game closed.
	settleTime = 15 * time.Minute
	// Big folders are only sampled; saves are rarely this many files.
	maxInspect = 5000
)

type folderState struct {
	tracked int    // files here that Steam Cloud tracks
	modSave string // extension of an untracked mod co-save, if any
	changed bool   // saves changed after Steam last synced them
}

type seenFile struct {
	path, dir, ext string // lower case
	mod            time.Time
}

// inspect compares the files in dir with what Steam Cloud last synced.
func inspect(dir string, tracked []entry) folderState {
	var st folderState
	byName := map[string][]entry{}
	for _, e := range tracked {
		n := strings.ToLower(filepath.Base(e.rel))
		byName[n] = append(byName[n], e)
	}
	exts := map[string]bool{}
	dirs := map[string]bool{}    // folders holding tracked files
	parents := map[string]bool{} // their parents: sibling save slots
	var newest time.Time
	var others []seenFile
	settled := time.Now().Add(-settleTime)
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if n++; n > maxInspect {
			return filepath.SkipAll
		}
		name := strings.ToLower(d.Name())
		if name == "steam_autocloud.vdf" {
			return nil
		}
		lp := strings.ToLower(filepath.Clean(p))
		var e entry
		ok := false
		for _, c := range byName[name] {
			if strings.HasSuffix(lp, `\`+c.rel) {
				e, ok = c, true
				break
			}
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		f := seenFile{lp, filepath.Dir(lp), filepath.Ext(name), info.ModTime()}
		if !ok {
			if modSaveExts[f.ext] && st.modSave == "" {
				st.modSave = f.ext
			}
			if len(others) < maxInspect {
				others = append(others, f)
			}
			return nil
		}
		st.tracked++
		exts[f.ext] = true
		dirs[f.dir] = true
		parents[filepath.Dir(f.dir)] = true
		if e.localTime.After(newest) {
			newest = e.localTime
		}
		// Steam records the file's time when it syncs; newer means it changed
		// while Steam wasn't watching.
		if !e.localTime.IsZero() && f.mod.Before(settled) && f.mod.After(e.localTime.Add(syncSlack)) {
			st.changed = true
		}
		return nil
	})
	if st.changed || newest.IsZero() {
		return st
	}
	// A new save (same kind of file, next to or beside the tracked ones) that
	// Steam never picked up.
	for _, f := range others {
		if exts[f.ext] && (dirs[f.dir] || parents[filepath.Dir(f.dir)]) &&
			f.mod.Before(settled) && f.mod.After(newest.Add(syncSlack)) {
			st.changed = true
			break
		}
	}
	return st
}
