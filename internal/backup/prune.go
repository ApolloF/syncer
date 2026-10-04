package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
// KeepDays is the longest setting of the PCs backing a folder up (keepFor),
// and nothing is pruned while this PC's clock is off (clockErr).
//
// Points saved from outside the backup (the saves a PC had before it started
// syncing, the files a restore replaced, the losing copy of a conflict) may
// hold saves the backup never had, and a point that took most of a folder's
// files at once (see mirror) may hold saves nobody meant to lose, so they are
// pinned: never thinned, and
// kept for a year (or KeepDays, if longer), since they may be the only copy
// left of those saves. A pin is an empty file
// <target>\.versions\<id>\.pinned\<stamp>.
const (
	PinnedDir  = ".pinned"
	keepPinned = 365 * 24 * time.Hour
	keepAll    = 24 * time.Hour
	keepDaily  = 7 * 24 * time.Hour
	// A PC that backed a folder up within this long counts when choosing the
	// one PC that thins its history.
	thinnerFresh = 14 * 24 * time.Hour
)

func prune(target string, keepDays int, now time.Time) {
	ids, _ := os.ReadDir(filepath.Join(target, VersionsDir))
	for _, id := range ids {
		if id.IsDir() && paths.ValidID(id.Name()) {
			pruneFolder(target, id.Name(), keepFor(target, id.Name(), keepDays, now), now, thinner(target, id.Name(), now))
		}
	}
}

// maxKeepDays caps how long another PC's info can ask history to be kept.
// Info files aren't trusted, and a wish past this only costs space.
const maxKeepDays = 10 * 365

// keepFor is how many days id's history is kept: the longest of this PC's
// setting and those of the other PCs backing the folder up here, each for as
// long as that PC may still want it (it backed up within its own setting).
// Any PC may expire points, so without this the PC with the shortest setting
// would decide for all of them. Only PCs using the same backup see each
// other's infos: PCs backing up to another Google account don't count.
func keepFor(target, id string, keepDays int, now time.Time) int {
	for _, in := range ReadInfos(target, id) {
		d := min(in.KeepDays, maxKeepDays)
		if !in.mine && d > keepDays && now.Sub(in.BackedUp) < time.Duration(d)*24*time.Hour {
			keepDays = d
		}
	}
	return keepDays
}

// pruneFolder expires id's points older than keepDays and, with thin, thins
// the rest (see above).
func pruneFolder(target, id string, keepDays int, now time.Time, thin bool) {
	root := filepath.Join(target, VersionsDir, id)
	pts := Points(target, id) // newest first
	cut := now.AddDate(0, 0, -keepDays)
	pinCut := now.Add(-keepPinned)
	if cut.Before(pinCut) {
		pinCut = cut
	}
	keep := plan(pts, aged(root, pts, cut), marks(root, PinnedDir), cut, pinCut, now, thin)
	aside := marks(root, AsideDir)
	last := "" // nearest older point that stays, made by a backup run
	for i := len(pts) - 1; i >= 0; i-- {
		stamp := pts[i].Format(stampFmt)
		dir := filepath.Join(root, stamp)
		switch {
		case keep[i]:
			// An aside point only counts as the point restored to, so a
			// thinned point's files must not go into it.
			if !aside[stamp] {
				last = dir
			}
		case pts[i].Before(cut):
			_ = os.RemoveAll(dir)
		case last != "":
			if merge(dir, last) == nil {
				mergeOrigin(dir, last)
			}
		}
	}
	unmarkStale(root, PinnedDir)
	unmarkStale(root, AsideDir)
	forgetStaleOrigins(root)
}

// aged gives each of pts the time it ages from. A point is named by the
// clock of the PC that made it, which may have been far behind (a flat CMOS
// battery): a point that arrived here after cut isn't expired by its name
// alone, it ages from when it arrived (its folder was made on this disk).
func aged(root string, pts []time.Time, cut time.Time) []time.Time {
	out := make([]time.Time, len(pts))
	for i, t := range pts {
		out[i] = t
		if t.Before(cut) {
			if at, ok := arrived(filepath.Join(root, t.Format(stampFmt))); ok && at.After(t) {
				out[i] = at
			}
		}
	}
	return out
}

// arrived is when the folder at p was made on this disk.
func arrived(p string) (time.Time, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, d.CreationTime.Nanoseconds()), true
}

// plan says which of pts (newest first, aging from at) stay: everything
// from cut on (thinned with thin), and pinned points from pinCut on.
func plan(pts, at []time.Time, pinned map[string]bool, cut, pinCut, now time.Time, thin bool) []bool {
	keep := make([]bool, len(pts))
	taken := map[string]bool{} // buckets that already have their point
	// Oldest first: a bucket's first point stays.
	for i := len(pts) - 1; i >= 0; i-- {
		t := pts[i]
		if at[i].Before(cut) {
			keep[i] = pinned[t.Format(stampFmt)] && !at[i].Before(pinCut)
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
func pin(target, id string, t time.Time) { pinStamp(target, id, t.Format(stampFmt)) }

func pinStamp(target, id, stamp string) { markStamp(target, id, PinnedDir, stamp) }

// markStamp marks id's point stamp in the folder sub (PinnedDir, AsideDir).
func markStamp(target, id, sub, stamp string) {
	p := filepath.Join(target, VersionsDir, id, sub, stamp)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = os.WriteFile(p, nil, 0o644)
	}
}

func pins(root string) map[string]bool { return marks(root, PinnedDir) }

// marks lists the stamps marked in root's folder sub.
func marks(root, sub string) map[string]bool {
	m := map[string]bool{}
	es, _ := os.ReadDir(filepath.Join(root, sub))
	for _, e := range es {
		m[e.Name()] = true
	}
	return m
}

// unmarkStale drops the marks in sub whose point is gone.
func unmarkStale(root, sub string) {
	dir := filepath.Join(root, sub)
	for name := range marks(root, sub) {
		if !isDir(filepath.Join(root, name)) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	_ = os.Remove(dir) // only if empty
}
