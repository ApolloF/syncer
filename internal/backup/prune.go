package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// A folder's restore points are thinned as they age, so a game that's backed
// up every few hours doesn't keep dozens of near-identical copies of big saves:
//   - points from the last day all stay;
//   - up to a week old, the oldest point of each day stays;
//   - after that, the oldest point of each week, until KeepDays.
//
// A point that goes is merged into the nearest older point that stays: the
// files that point lacks move there, the rest are dropped. Versions hold what
// a file was before a run replaced it, so a file the older point lacks hadn't
// changed in between and the moved copy is exactly what it was then: every
// remaining point restores the same as before. Only points older than
// KeepDays are deleted outright; the backup itself always keeps the latest.
//
// Points saved from outside the backup (the saves a PC had before it started
// syncing, the files a restore replaced, the losing copy of a conflict) may
// hold saves the backup never had, so they are pinned: never thinned, only
// expired. A pin is an empty file <target>\.versions\<id>\.pinned\<stamp>.
const (
	PinnedDir = ".pinned"
	keepAll   = 24 * time.Hour
	keepDaily = 7 * 24 * time.Hour
	// A PC that backed a folder up within this long counts when choosing the
	// one PC that thins its history.
	thinnerFresh = 14 * 24 * time.Hour
)

func prune(target string, keepDays int, now time.Time) {
	ids, _ := os.ReadDir(filepath.Join(target, VersionsDir))
	for _, id := range ids {
		if id.IsDir() && paths.ValidID(id.Name()) {
			pruneFolder(target, id.Name(), keepDays, now, thinner(target, id.Name(), now))
		}
	}
}

// pruneFolder expires id's points older than keepDays and, with thin, thins
// the rest (see above).
func pruneFolder(target, id string, keepDays int, now time.Time, thin bool) {
	root := filepath.Join(target, VersionsDir, id)
	pts := Points(target, id) // newest first
	cut := now.AddDate(0, 0, -keepDays)
	keep := plan(pts, pins(root), cut, now, thin)
	last := "" // nearest older point that stays
	for i := len(pts) - 1; i >= 0; i-- {
		dir := filepath.Join(root, pts[i].Format(stampFmt))
		switch {
		case keep[i]:
			last = dir
		case pts[i].Before(cut):
			_ = os.RemoveAll(dir)
		case last != "":
			_ = merge(dir, last)
		}
	}
	unpinStale(root)
}

// plan says which of pts (newest first) stay.
func plan(pts []time.Time, pinned map[string]bool, cut, now time.Time, thin bool) []bool {
	keep := make([]bool, len(pts))
	taken := map[string]bool{} // buckets that already have their point
	// Oldest first: a bucket's first point stays.
	for i := len(pts) - 1; i >= 0; i-- {
		t := pts[i]
		if t.Before(cut) {
			continue
		}
		b := bucket(t, now.Sub(t))
		keep[i] = !thin || now.Sub(t) < keepAll || pinned[t.Format(stampFmt)] || !taken[b]
		taken[b] = true
	}
	return keep
}

// bucket names the day or week a point of the given age is thinned within.
func bucket(t time.Time, age time.Duration) string {
	if age < keepDaily {
		return t.Format("2006-01-02")
	}
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// merge moves src's files that dst lacks into dst, then removes src. If a
// file can't be moved, src stays (minus what moved) and the next run retries.
func merge(src, dst string) error {
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if _, err := os.Lstat(out); err == nil {
			return os.Remove(p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return moveTo(p, out)
	})
	if err != nil {
		removeEmptyDirs(src)
		return err
	}
	return os.RemoveAll(src)
}

// thinner reports whether this PC thins id's history. Several PCs can back up
// the same folder, and thinning moves files between points, so only one does:
// the PC with the first name among those that backed it up lately. Expiring
// old points is plain deleting, which any PC may do.
func thinner(target, id string, now time.Time) bool {
	dir := filepath.Join(target, InfoDir, id)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || key >= hostKey {
			continue
		}
		if in, err := readInfo(filepath.Join(dir, e.Name())); err == nil && in.ID == id && now.Sub(in.BackedUp) < thinnerFresh {
			return false
		}
	}
	return true
}

// pin keeps the point at t from being thinned.
func pin(target, id string, t time.Time) {
	p := filepath.Join(target, VersionsDir, id, PinnedDir, t.Format(stampFmt))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = os.WriteFile(p, nil, 0o644)
	}
}

func pins(root string) map[string]bool {
	m := map[string]bool{}
	es, _ := os.ReadDir(filepath.Join(root, PinnedDir))
	for _, e := range es {
		m[e.Name()] = true
	}
	return m
}

// unpinStale drops pins whose point is gone.
func unpinStale(root string) {
	dir := filepath.Join(root, PinnedDir)
	for name := range pins(root) {
		if !isDir(filepath.Join(root, name)) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	_ = os.Remove(dir) // only if empty
}
