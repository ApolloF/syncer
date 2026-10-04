package backup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// asideFixture backs up a folder with x.sav and y.sav, then changes y.sav
// and backs up again: point T1 holds y.sav as it was before.
func asideFixture(t *testing.T) (target string, f Folder, t1 time.Time) {
	t.Helper()
	src, target := t.TempDir(), t.TempDir()
	f = Folder{ID: "aside-" + time.Now().Format("150405.000000"), Label: "Game", Path: src}
	t.Cleanup(func() { os.Remove(indexPath(f.ID)) })
	write(t, filepath.Join(src, "x.sav"), "kept")
	write(t, filepath.Join(src, "y.sav"), "y1")
	run := func() {
		t.Helper()
		if res, err := Run(context.Background(), []Folder{f}, Options{Target: target}); err != nil || !res.OK {
			t.Fatalf("run: %v %+v", err, res)
		}
	}
	run()
	write(t, filepath.Join(src, "y.sav"), "y2, longer")
	run()
	pts := Points(target, f.ID)
	if len(pts) != 1 {
		t.Fatalf("want 1 point, got %v", pts)
	}
	return target, f, pts[0]
}

// The user settled a conflict on x.sav by keeping their own file, and the
// other PC's copy went into the history. Restoring to before an earlier
// backup must give x.sav as it was then, not the copy they turned down.
func TestRestoreOlderPointLeavesOutConflictLoser(t *testing.T) {
	target, f, t1 := asideFixture(t)
	loser := filepath.Join(t.TempDir(), "x.sav")
	write(t, loser, "LOSER-from-other-pc")
	kept, err := Keep(target, f.ID, loser, "x.sav", true)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(target, f, t1); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(f.Path, "x.sav")); got != "kept" {
		t.Errorf("x.sav restored to %q, want %q", got, "kept")
	}
	if got := read(t, filepath.Join(f.Path, "y.sav")); got != "y1" {
		t.Errorf("y.sav restored to %q, want %q", got, "y1")
	}

	// Restoring to the kept copy's own point still brings it back.
	pt, err := time.ParseInLocation(stampFmt, filepath.Base(filepath.Dir(kept)), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, f, pt); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(f.Path, "x.sav")); got != "LOSER-from-other-pc" {
		t.Errorf("restoring the kept copy's point gave x.sav %q", got)
	}
}

// A restore's safety point and a snapshot are aside points too.
func TestAsidePointsAreMarked(t *testing.T) {
	target, f, t1 := asideFixture(t)
	if _, err := Restore(target, f, t1); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.Path, "new.sav"), "not backed up yet")
	if _, err := Snapshot(context.Background(), target, f); err != nil {
		t.Fatal(err)
	}
	aside := marks(filepath.Join(target, VersionsDir, f.ID), AsideDir)
	n := 0
	for _, p := range Points(target, f.ID) {
		if aside[p.Format(stampFmt)] {
			n++
		} else if !p.Equal(t1) {
			t.Errorf("point %v isn't marked aside", p)
		}
	}
	if n != 2 || aside[t1.Format(stampFmt)] {
		t.Errorf("%d aside points (want 2), the run's marked: %v", n, aside[t1.Format(stampFmt)])
	}
}

// Thinning never moves a run's files into an aside point, where restores
// would no longer see them, nor past one into an older point: restoring to
// the aside point lays the run's files over it, so it stays as it was.
func TestPruneDoesNotMergeIntoAsidePoint(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	// An aside point between B and C, which would be thinned into B.
	a := at["B"].Add(30 * time.Minute)
	dir := filepath.Join(target, VersionsDir, id, a.Format(stampFmt))
	write(t, filepath.Join(dir, "other.sav"), "aside")
	pin(target, id, a)
	markStamp(target, id, AsideDir, a.Format(stampFmt))
	before := state(t, target, id, a)

	pruneFolder(target, id, 30, now, true)
	pts := points(target, id)
	if !pts[a] {
		t.Fatal("aside point thinned")
	}
	if !pts[at["C"]] {
		t.Fatal("C thinned past the aside point")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "y.sav")); err == nil {
		t.Error("C merged into the aside point")
	}
	b := filepath.Join(target, VersionsDir, id, at["B"].Format(stampFmt))
	if _, err := os.Stat(filepath.Join(b, "sub", "y.sav")); err == nil {
		t.Error("C merged into B")
	}
	if got := state(t, target, id, a); !reflect.DeepEqual(got, before) {
		t.Errorf("restoring the aside point gives %v, was %v", got, before)
	}
}

// Without an aside point in between, a thinned point still goes into the
// nearest older run point.
func TestPruneMergesPastAsidePointOutsideTheGap(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	// An aside point a week before B: B to C has none between them.
	a := at["B"].AddDate(0, 0, -8)
	write(t, filepath.Join(target, VersionsDir, id, a.Format(stampFmt), "other.sav"), "aside")
	pin(target, id, a)
	markStamp(target, id, AsideDir, a.Format(stampFmt))

	pruneFolder(target, id, 30, now, true)
	if points(target, id)[at["C"]] {
		t.Fatal("C not thinned")
	}
	b := filepath.Join(target, VersionsDir, id, at["B"].Format(stampFmt))
	if read(t, filepath.Join(b, "sub", "y.sav")) != "y0" {
		t.Error("C not merged into B")
	}
}

// A point whose name says it's over a year old, from a PC whose clock was
// far behind, but which only just arrived here, isn't expired.
func TestPruneKeepsPointThatJustArrived(t *testing.T) {
	target, id := t.TempDir(), "late-point"
	now := time.Now()
	old := now.AddDate(0, 0, -400)
	write(t, filepath.Join(target, VersionsDir, id, old.Format(stampFmt), "a.sav"), "made with a wrong clock")
	pruneFolder(target, id, 30, now, true)
	if !points(target, id)[old.Truncate(time.Second)] {
		t.Fatal("point that just arrived expired by its name")
	}
	// Once it has been here past the history setting, it goes.
	pruneFolder(target, id, 30, now.AddDate(0, 0, 31), true)
	if len(Points(target, id)) != 0 {
		t.Fatal("point kept past the history setting")
	}
}

// Two points from a PC whose clock was far behind, in the same week by their
// names, arrive together: the newer one is thinned into the older one, not
// deleted as expired.
func TestPruneMergesLatePointsOfOneWeek(t *testing.T) {
	target, id := t.TempDir(), "late-week"
	now := time.Now()
	old := now.AddDate(0, 0, -400)
	a := time.Date(old.Year(), old.Month(), old.Day(), 10, 0, 0, 0, time.Local)
	a = a.AddDate(0, 0, int(time.Wednesday-a.Weekday()))
	b := a.Add(time.Hour)
	write(t, filepath.Join(target, VersionsDir, id, a.Format(stampFmt), "a.sav"), "a")
	write(t, filepath.Join(target, VersionsDir, id, b.Format(stampFmt), "b.sav"), "b")

	pruneFolder(target, id, 30, now, true)
	pts := points(target, id)
	if !pts[a] || pts[b] || len(pts) != 1 {
		t.Fatalf("points %v, want only %v", pts, a)
	}
	if got := read(t, filepath.Join(target, VersionsDir, id, a.Format(stampFmt), "b.sav")); got != "b" {
		t.Errorf("b.sav = %q, want it merged into the older point", got)
	}
}

// Drive can bring a point's marks before the point itself: they wait for it,
// so it arrives pinned and aside. A mark whose point never comes, or which
// this PC removed, goes.
func TestPruneKeepsMarksOfPointNotHereYet(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	root := filepath.Join(target, VersionsDir, id)
	late := at["B"].Add(30 * time.Minute) // thinned into B, were it not pinned
	stamp := late.Format(stampFmt)
	pin(target, id, late)
	markStamp(target, id, AsideDir, stamp)
	expired := at["A"].Format(stampFmt) // expired here: its mark goes at once
	markStamp(target, id, AsideDir, expired)

	pruneFolder(target, id, 30, now, true)
	if !marks(root, PinnedDir)[stamp] || !marks(root, AsideDir)[stamp] {
		t.Fatal("marks of a point not here yet dropped")
	}
	if points(target, id)[at["A"]] || marks(root, AsideDir)[expired] {
		t.Fatal("expired point or its mark kept")
	}
	write(t, filepath.Join(root, stamp, "other.sav"), "aside")
	pruneFolder(target, id, 30, now, true)
	if !points(target, id)[late] {
		t.Fatal("point thinned once it arrived")
	}
	if !marks(root, AsideDir)[stamp] {
		t.Fatal("point lost its aside mark")
	}

	// A mark long without its point goes.
	never := at["D"].Add(time.Hour).Format(stampFmt)
	markStamp(target, id, AsideDir, never)
	madeAt(t, filepath.Join(root, AsideDir, never), now.Add(-markGrace-time.Hour))
	pruneFolder(target, id, 30, now, true)
	if marks(root, AsideDir)[never] {
		t.Error("mark without its point kept past the grace period")
	}
}

// A point holding two copies of one file (see moveTo) restores the file,
// never a *.syncer-kept-N copy into the game's folder.
func TestRestoreSkipsSecondCopies(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	f := Folder{ID: "kept-copy", Label: "Game", Path: src}
	pt := time.Now().Add(-time.Hour).Truncate(time.Second)
	dir := filepath.Join(target, VersionsDir, f.ID, pt.Format(stampFmt))
	write(t, filepath.Join(dir, "a.sav"), "newer")
	write(t, filepath.Join(dir, "a.sav.syncer-kept-1759500000000000000"), "older")
	write(t, filepath.Join(target, f.ID, "a.sav"), "latest")
	if _, err := Restore(target, f, pt); err != nil {
		t.Fatal(err)
	}
	es, _ := os.ReadDir(src)
	for _, e := range es {
		if strings.Contains(e.Name(), ".syncer-kept-") {
			t.Errorf("restore wrote %s into the game's folder", e.Name())
		}
	}
	if got := read(t, filepath.Join(src, "a.sav")); got != "newer" {
		t.Errorf("a.sav = %q", got)
	}
}

// A backup run never adds its versions to a point made in the same second
// (a kept conflict copy, a snapshot).
func TestRunMakesAPointOfItsOwn(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	f := Folder{ID: "own-point-" + time.Now().Format("150405.000000"), Label: "Game", Path: src}
	t.Cleanup(func() { os.Remove(indexPath(f.ID)) })
	write(t, filepath.Join(src, "a.sav"), "a1")
	if res, err := Run(context.Background(), []Folder{f}, Options{Target: target}); err != nil || !res.OK {
		t.Fatalf("run: %v %+v", err, res)
	}
	write(t, filepath.Join(src, "a.sav"), "a2, longer")
	// Aside points for the next few seconds: the run starts in one of them.
	now := time.Now()
	var taken []string
	for i := range 3 {
		stamp := now.Add(time.Duration(i) * time.Second).Format(stampFmt)
		write(t, filepath.Join(target, VersionsDir, f.ID, stamp, "conflict.sav"), "kept copy")
		markStamp(target, f.ID, AsideDir, stamp)
		taken = append(taken, stamp)
	}
	if res, err := Run(context.Background(), []Folder{f}, Options{Target: target}); err != nil || !res.OK {
		t.Fatalf("run: %v %+v", err, res)
	}
	for _, stamp := range taken {
		if _, err := os.Stat(filepath.Join(target, VersionsDir, f.ID, stamp, "a.sav")); err == nil {
			t.Errorf("the run put a.sav into the point at %s", stamp)
		}
	}
	if len(Points(target, f.ID)) != 4 {
		t.Errorf("points: %v", Points(target, f.ID))
	}
}
