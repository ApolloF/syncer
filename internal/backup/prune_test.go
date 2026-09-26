package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/store"
)

// pruneFixture lays out a history with a known shape. now is a Wednesday.
//
//	A  -40d             expired
//	B  -20d  (Thu)      x0            oldest of its week: stays
//	C  -20d +1h         x1 y0         same week as B: merged into B
//	D  -5d              w0            oldest of its day: stays
//	E  -5d +3h          x2 z0         same day as D: merged into D
//	F  -2d                            own day: stays
//	G..I  last 3 hours                within a day: stay
func pruneFixture(t *testing.T) (target, id string, now time.Time, at map[string]time.Time) {
	t.Helper()
	target, id = t.TempDir(), "prune-test"
	now = time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	at = map[string]time.Time{
		"A": now.AddDate(0, 0, -40),
		"B": now.AddDate(0, 0, -20),
		"C": now.AddDate(0, 0, -20).Add(time.Hour),
		"D": now.AddDate(0, 0, -5),
		"E": now.AddDate(0, 0, -5).Add(3 * time.Hour),
		"F": now.AddDate(0, 0, -2),
		"G": now.Add(-3 * time.Hour),
		"H": now.Add(-2 * time.Hour),
		"I": now.Add(-time.Hour),
	}
	files := map[string]map[string]string{
		"A": {"x.sav": "old"},
		"B": {"x.sav": "x0"},
		"C": {"x.sav": "x1", "sub/y.sav": "y0"},
		"D": {"w.sav": "w0"},
		"E": {"x.sav": "x2", "z.sav": "z0"},
		"F": {"x.sav": "x3"},
		"G": {"x.sav": "x4"},
		"H": {"x.sav": "x5"},
		"I": {"x.sav": "x6"},
	}
	for name, fs := range files {
		for rel, s := range fs {
			write(t, filepath.Join(target, VersionsDir, id, at[name].Format(stampFmt), rel), s)
		}
	}
	for rel, s := range map[string]string{"x.sav": "now", "sub/y.sav": "y-now", "z.sav": "z-now", "w.sav": "w-now"} {
		write(t, filepath.Join(target, id, rel), s)
	}
	return
}

// state is what restoring id to point would write.
func state(t *testing.T, target, id string, point time.Time) map[string]string {
	t.Helper()
	src, _ := sources(target, id, point)
	out := map[string]string{}
	for k, p := range src {
		out[k] = read(t, p)
	}
	return out
}

func points(target, id string) map[time.Time]bool {
	m := map[time.Time]bool{}
	for _, p := range Points(target, id) {
		m[p] = true
	}
	return m
}

func TestPruneThinsWithoutChangingRemainingPoints(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	stay := []string{"B", "D", "F", "G", "H", "I"}
	before := map[string]map[string]string{}
	for _, n := range stay {
		before[n] = state(t, target, id, at[n])
	}

	pruneFolder(target, id, 30, now, true)

	got := points(target, id)
	if len(got) != len(stay) {
		t.Fatalf("want %d points, got %d: %v", len(stay), len(got), Points(target, id))
	}
	for _, n := range stay {
		if !got[at[n]] {
			t.Errorf("point %s was removed", n)
		}
		after := state(t, target, id, at[n])
		if len(after) != len(before[n]) {
			t.Errorf("point %s: files %v, want %v", n, after, before[n])
		}
		for k, v := range before[n] {
			if after[k] != v {
				t.Errorf("point %s: %s = %q, want %q", n, k, after[k], v)
			}
		}
	}
	// The dropped versions are gone, the ones older points lacked moved.
	b := filepath.Join(target, VersionsDir, id, at["B"].Format(stampFmt))
	if read(t, filepath.Join(b, "x.sav")) != "x0" || read(t, filepath.Join(b, "sub", "y.sav")) != "y0" {
		t.Error("C not merged into B")
	}
	d := filepath.Join(target, VersionsDir, id, at["D"].Format(stampFmt))
	if read(t, filepath.Join(d, "x.sav")) != "x2" || read(t, filepath.Join(d, "z.sav")) != "z0" {
		t.Error("E not merged into D")
	}

	// A second pass changes nothing.
	pruneFolder(target, id, 30, now, true)
	if len(points(target, id)) != len(stay) {
		t.Fatal("second prune removed more points")
	}
}

func TestPrunePinnedPointsStay(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	pin(target, id, at["C"])
	pin(target, id, at["A"])
	pruneFolder(target, id, 30, now, true)
	got := points(target, id)
	if !got[at["C"]] {
		t.Error("pinned point C was thinned")
	}
	if got[at["A"]] || got[at["E"]] {
		t.Error("pin kept an expired point, or E wasn't thinned")
	}
	if pins(filepath.Join(target, VersionsDir, id))[at["A"].Format(stampFmt)] {
		t.Error("stale pin for expired point A left behind")
	}
}

func TestPruneExpiresEverythingPastKeepDays(t *testing.T) {
	target, id, now, _ := pruneFixture(t)
	pruneFolder(target, id, 30, now.AddDate(1, 0, 0), true)
	if n := len(Points(target, id)); n != 0 {
		t.Fatalf("want no points left, got %d", n)
	}
	if read(t, filepath.Join(target, id, "x.sav")) != "now" {
		t.Fatal("backup touched")
	}
}

func TestPruneOnlyOnePCThins(t *testing.T) {
	target, id, now, at := pruneFixture(t)
	other := infoPath(target, id, "0000") // sorts before any real PC name
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(other, Info{ID: id, Host: "0000", BackedUp: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if thinner(target, id, now) {
		t.Fatal("this PC thins though another PC comes first")
	}
	if !thinner(target, id, now.Add(thinnerFresh+time.Hour)) {
		t.Fatal("a PC that stopped backing up still blocks thinning")
	}

	prune(target, 30, now)
	got := points(target, id)
	if got[at["A"]] {
		t.Error("expired point kept")
	}
	if !got[at["C"]] || !got[at["E"]] {
		t.Error("history thinned by a PC that isn't the thinner")
	}
}

func TestRestoreSkipsUnchangedFiles(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "restore-skip"
	f := Folder{ID: id, Label: "Test", Path: src}
	write(t, filepath.Join(target, id, "a.sav"), "a")
	write(t, filepath.Join(target, id, "b.sav"), "b")
	write(t, filepath.Join(src, "a.sav"), "a")

	if n, err := Restore(target, f, time.Time{}); err != nil || n != 2 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
	if len(Points(target, id)) != 0 {
		t.Fatal("restore saved files that it didn't change")
	}

	write(t, filepath.Join(src, "b.sav"), "changed")
	if _, err := Restore(target, f, time.Time{}); err != nil {
		t.Fatal(err)
	}
	pts := Points(target, id)
	if len(pts) != 1 {
		t.Fatalf("want 1 undo point, got %d", len(pts))
	}
	dir := filepath.Join(target, VersionsDir, id, pts[0].Format(stampFmt))
	if read(t, filepath.Join(dir, "b.sav")) != "changed" {
		t.Error("changed file not saved before restore")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.sav")); err == nil {
		t.Error("unchanged file saved before restore")
	}
	if !pins(filepath.Join(target, VersionsDir, id))[pts[0].Format(stampFmt)] {
		t.Error("undo point not pinned")
	}
	if read(t, filepath.Join(src, "b.sav")) != "b" {
		t.Error("b not restored")
	}
}
