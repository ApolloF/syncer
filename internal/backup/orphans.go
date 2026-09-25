package backup

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Orphan is a backup under the target that belongs to no folder this PC
// syncs or backs up: a game removed here, or one another PC backs up.
type Orphan struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`    // from its info files, or made from the id
	Path     string    `json:"path"`     // where it was, resolved on this PC ("" if unknown)
	Host     string    `json:"host"`     // the PC that backed it up last ("" if unknown)
	Mine     bool      `json:"mine"`     // that PC is this one
	BackedUp time.Time `json:"backedUp"` // its last backup (zero if unknown)
	Modified time.Time `json:"modified"` // its newest save in the backup
	Bytes    int64     `json:"bytes"`
	Files    int       `json:"files"`
	Points   int       `json:"points"` // restore points
}

// Orphans lists the backups under target whose id known doesn't claim. Sizes
// come from walking the backup (capped), so call it sparingly.
func Orphans(ctx context.Context, target string, known func(id string) bool) []Orphan {
	ids := map[string]bool{}
	for _, dir := range []string{target, filepath.Join(target, VersionsDir)} {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if n := e.Name(); e.IsDir() && !strings.HasPrefix(n, ".") && paths.ValidID(n) && !known(n) {
				ids[n] = true
			}
		}
	}
	out := make([]Orphan, 0, len(ids))
	for id := range ids {
		if ctx.Err() != nil {
			break
		}
		infos := ReadInfos(target, id)
		o := Orphan{ID: id, Label: labelFrom(infos, id), Points: len(Points(target, id))}
		if len(infos) > 0 {
			o.Host, o.Mine, o.BackedUp = infos[0].Host, infos[0].Mine(), infos[0].BackedUp
		}
		for _, in := range infos { // prefer where this PC had it
			if p, ok := in.Path(); ok && (o.Path == "" || in.Mine()) {
				o.Path = p
			}
		}
		_ = filepath.WalkDir(filepath.Join(target, id), func(_ string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if o.Files >= 20000 || ctx.Err() != nil {
				return filepath.SkipAll
			}
			if fi, err := d.Info(); err == nil {
				o.Bytes += fi.Size()
				if fi.ModTime().After(o.Modified) {
					o.Modified = fi.ModTime()
				}
			}
			o.Files++
			return nil
		})
		if o.Files == 0 && o.Points == 0 {
			continue // an empty folder left behind: nothing to restore
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// LabelFor names a backup: the game's name from its newest info file, or a
// guess from its id.
func LabelFor(target, id string) string { return labelFrom(ReadInfos(target, id), id) }

func labelFrom(infos []Info, id string) string {
	for _, in := range infos {
		if in.Label != "" {
			return in.Label
		}
	}
	return LabelFromID(id)
}
