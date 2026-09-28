package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

// OriginsDir, next to a folder's restore points, holds one small file per
// point (<stamp>.json) saying which PC made the point and on which PCs its
// saves were last changed: synced saves are backed up by every PC, so the
// date alone doesn't tell where they came from.
const OriginsDir = ".origins"

// maxOrigins caps how many files' origins a point looks up (one Syncthing
// call each); a mod folder can hold thousands.
const maxOrigins = 100

// Origin is where a restore point came from.
type Origin struct {
	By   []string `json:"by,omitempty"`   // the PC(s) that made it
	From []string `json:"from,omitempty"` // the PCs its saves were last changed on, as far as known
}

// FileOrigin, if set, names the PC that last changed a synced folder's file
// (rel with forward slashes), "" when unknown.
var FileOrigin func(id, rel string) string

// HostName is this PC's name, as backups record it.
func HostName() string { return hostName }

// origins collects the PCs a point's files came from.
type origins struct {
	id      string
	solo    bool
	from    map[string]bool
	lookups int
}

func newOrigins(id string, solo bool) *origins {
	return &origins{id: id, solo: solo, from: map[string]bool{}}
}

// lookup names the PC a file of the folder was last changed on right now.
func (o *origins) lookup(rel string) string {
	if o.solo {
		return hostName // backed up only, never synced: this PC's own saves
	}
	if FileOrigin == nil || o.lookups >= maxOrigins {
		return ""
	}
	o.lookups++
	return FileOrigin(o.id, filepath.ToSlash(rel))
}

func (o *origins) add(pc string) {
	if pc != "" {
		o.from[cleanText(pc, 64)] = true
	}
}

// save records the point's origin, if it has any files.
func (o *origins) save(target, stamp string) {
	writeOrigin(target, o.id, stamp, Origin{By: []string{hostName}, From: setList(o.from)})
}

func originPath(target, id, stamp string) string {
	return filepath.Join(target, VersionsDir, id, OriginsDir, stamp+".json")
}

// writeOrigin records in's origin for the point stamp, joined with what is
// already recorded (two PCs can make a point in the same second).
func writeOrigin(target, id, stamp string, in Origin) {
	if !paths.ValidID(id) || !isDir(filepath.Join(target, VersionsDir, id, stamp)) {
		return
	}
	p := originPath(target, id, stamp)
	if old, err := readOrigin(p); err == nil {
		in = joinOrigins(old, in)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = store.WriteJSON(p, in)
	}
}

func readOrigin(p string) (Origin, error) {
	var in Origin
	fi, err := os.Lstat(p)
	if err != nil {
		return in, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxInfo {
		return in, errors.New("not an origin file")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, err
	}
	// Written by any PC using the same backup folder: not trusted.
	clean := func(l []string) []string {
		var out []string
		for _, s := range l {
			if s = cleanText(s, 64); s != "" && len(out) < 16 && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		return out
	}
	in.By, in.From = clean(in.By), clean(in.From)
	return in, nil
}

func joinOrigins(a, b Origin) Origin {
	join := func(x, y []string) []string {
		m := map[string]bool{}
		for _, s := range append(slices.Clone(x), y...) {
			m[s] = true
		}
		return setList(m)
	}
	return Origin{By: join(a.By, b.By), From: join(a.From, b.From)}
}

func setList(m map[string]bool) []string {
	var out []string
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// PointOrigins returns the recorded origin of each of id's restore points
// (by point), and of its latest backup as far as this PC knows (the files
// it put there itself).
func PointOrigins(target, id string) (points map[time.Time]Origin, latest Origin) {
	points = map[time.Time]Origin{}
	if target == "" || !paths.ValidID(id) {
		return points, latest
	}
	dir := filepath.Join(target, VersionsDir, id, OriginsDir)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		stamp, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		t, err := time.ParseInLocation(stampFmt, stamp, time.Local)
		if err != nil {
			continue
		}
		if in, err := readOrigin(filepath.Join(dir, e.Name())); err == nil {
			points[t] = in
		}
	}
	from := map[string]bool{}
	for _, e := range loadIndex(id) {
		if e.O != "" {
			from[e.O] = true
		}
	}
	latest.From = setList(from)
	return points, latest
}

// mergeOrigin folds the origin of the point in dir into the point in into
// (see merge), and forgets it.
func mergeOrigin(dir, into string) {
	root, stamp := filepath.Dir(dir), filepath.Base(dir)
	p := filepath.Join(root, OriginsDir, stamp+".json")
	in, err := readOrigin(p)
	if err != nil {
		return
	}
	q := filepath.Join(root, OriginsDir, filepath.Base(into)+".json")
	if old, err := readOrigin(q); err == nil {
		in = joinOrigins(old, in)
	}
	if store.WriteJSON(q, in) == nil {
		_ = os.Remove(p)
	}
}

// forgetStaleOrigins removes the origins of points that are gone.
func forgetStaleOrigins(root string) {
	dir := filepath.Join(root, OriginsDir)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if stamp, ok := strings.CutSuffix(e.Name(), ".json"); ok && !isDir(filepath.Join(root, stamp)) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // only if empty
}
