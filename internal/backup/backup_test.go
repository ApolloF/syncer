package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestMatcher(t *testing.T) {
	m := NewMatcher(append(builtin, "Trainers/", "// comment", "/root-only.txt", "*.log", "(?d)cache", "!keep.log"))
	cases := map[string]bool{
		".stfolder":                 true,
		"Trainers":                  true,
		`Trainers\x\y.exe`:          true,
		"root-only.txt":             true,
		"sub/root-only.txt":         false,
		"a/b/debug.LOG":             true,
		"cache/x":                   true,
		"saves/slot1.sav":           false,
		"~syncthing~foo.tmp":        true,
		"x/.syncthing.a.sav.tmp":    true,
		"Saves/steam_autocloud.vdf": true,
	}
	for p, want := range cases {
		if got := m.Ignored(p); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRunMirrorVersionRestore(t *testing.T) {

	src := t.TempDir()
	target := t.TempDir()
	id := "test-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	f := Folder{ID: id, Label: "Test", Path: src}

	write(t, filepath.Join(src, "slot1.sav"), "v1")
	write(t, filepath.Join(src, "sub", "slot2.sav"), "a")
	write(t, filepath.Join(src, ".stignore"), "ignored/\n")
	write(t, filepath.Join(src, "ignored", "junk.bin"), "x")

	run := func() {
		t.Helper()
		res, err := Run(context.Background(), []Folder{f}, Options{Target: target, KeepDays: 30})
		if err != nil {
			t.Fatal(err)
		}
		if !res.OK {
			t.Fatalf("errors: %v", res.Errors)
		}
	}
	run()
	if read(t, filepath.Join(target, id, "slot1.sav")) != "v1" {
		t.Fatal("slot1 not copied")
	}
	if _, err := os.Stat(filepath.Join(target, id, "ignored", "junk.bin")); err == nil {
		t.Fatal("ignored file was backed up")
	}

	// Unchanged second run copies nothing.
	res, _ := Run(context.Background(), []Folder{f}, Options{Target: target})
	if res.Copied != 0 {
		t.Fatalf("expected no copies, got %d", res.Copied)
	}

	// Change + delete -> old copies land in versions.
	time.Sleep(1100 * time.Millisecond) // new version stamp
	write(t, filepath.Join(src, "slot1.sav"), "v2-longer")
	os.Remove(filepath.Join(src, "sub", "slot2.sav"))
	run()
	if read(t, filepath.Join(target, id, "slot1.sav")) != "v2-longer" {
		t.Fatal("slot1 not updated")
	}
	if _, err := os.Stat(filepath.Join(target, id, "sub", "slot2.sav")); err == nil {
		t.Fatal("deleted file still in backup")
	}
	pts := Points(target, id)
	if len(pts) != 1 {
		t.Fatalf("want 1 restore point, got %d", len(pts))
	}
	vdir := filepath.Join(target, VersionsDir, id, pts[0].Format(stampFmt))
	if read(t, filepath.Join(vdir, "slot1.sav")) != "v1" || read(t, filepath.Join(vdir, "sub", "slot2.sav")) != "a" {
		t.Fatal("versions missing old content")
	}

	// Empty source must not retire the backup.
	os.Remove(filepath.Join(src, "slot1.sav"))
	os.Remove(filepath.Join(src, ".stignore"))
	os.RemoveAll(filepath.Join(src, "ignored"))
	run()
	if _, err := os.Stat(filepath.Join(target, id, "slot1.sav")); err != nil {
		t.Fatal("backup wiped after source emptied")
	}

	// Restore to the point before v2: slot1=v1 and slot2 comes back.
	time.Sleep(1100 * time.Millisecond)
	n, err := Restore(target, f, pts[0])
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || read(t, filepath.Join(src, "slot1.sav")) != "v1" || read(t, filepath.Join(src, "sub", "slot2.sav")) != "a" {
		t.Fatalf("restore wrong: n=%d", n)
	}
	// Restore latest: slot1=v2.
	time.Sleep(1100 * time.Millisecond)
	if _, err := Restore(target, f, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(src, "slot1.sav")) != "v2-longer" {
		t.Fatal("restore latest wrong")
	}
}

func TestLockExclusive(t *testing.T) {
	u, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(); err != ErrBusy {
		t.Fatalf("second lock: %v", err)
	}
	u()
	if Running() {
		t.Fatal("still locked")
	}
}

func TestSnapshot(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	id := "snap-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	f := Folder{ID: id, Label: "Snap", Path: src}
	ctx := context.Background()

	if n, err := Snapshot(ctx, target, Folder{ID: id, Path: filepath.Join(src, "missing")}); n != 0 || err != nil {
		t.Fatalf("missing folder: n=%d err=%v", n, err)
	}

	write(t, filepath.Join(src, "slot1.sav"), "mine")
	write(t, filepath.Join(src, "sub", "slot2.sav"), "mine2")
	write(t, filepath.Join(src, ".stversions", "old.sav"), "x")
	n, err := Snapshot(ctx, target, f)
	if err != nil || n != 2 {
		t.Fatalf("snapshot: n=%d err=%v", n, err)
	}
	pts := Points(target, id)
	if len(pts) != 1 {
		t.Fatalf("want 1 restore point, got %d", len(pts))
	}

	// The other PC's save arrives and replaces ours; restoring to the
	// snapshot brings ours back.
	time.Sleep(1100 * time.Millisecond)
	write(t, filepath.Join(src, "slot1.sav"), "theirs")
	if _, err := Restore(target, f, pts[0]); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(src, "slot1.sav")) != "mine" || read(t, filepath.Join(src, "sub", "slot2.sav")) != "mine2" {
		t.Fatal("snapshot not restored")
	}

	// Files already in the mirror unchanged aren't copied again.
	if res, err := Run(ctx, []Folder{f}, Options{Target: target}); err != nil || !res.OK {
		t.Fatalf("run: %v %v", err, res)
	}
	if n, err := Snapshot(ctx, target, f); n != 0 || err != nil {
		t.Fatalf("unchanged snapshot: n=%d err=%v", n, err)
	}
}

func TestRunSkipsInvalidID(t *testing.T) {
	target := t.TempDir()
	res, err := Run(context.Background(), []Folder{{ID: `..\evil`, Label: "Evil", Path: t.TempDir()}}, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || len(res.Errors) != 1 || res.Folders != 0 {
		t.Fatalf("invalid id not rejected: %+v", res)
	}
}

func TestRunCallsPause(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "pause-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	write(t, filepath.Join(src, "a.sav"), "a")
	calls := 0
	_, err := Run(context.Background(), []Folder{{ID: id, Label: "P", Path: src}},
		Options{Target: target, Pause: func(context.Context) { calls++ }})
	if err != nil || calls == 0 {
		t.Fatalf("pause calls=%d err=%v", calls, err)
	}
}

func TestCancelledRunKeepsBackup(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "cancel-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	write(t, filepath.Join(src, "a.sav"), "a")
	write(t, filepath.Join(src, "b.sav"), "b")
	if _, err := Run(context.Background(), []Folder{{ID: id, Label: "C", Path: src}}, Options{Target: target}); err != nil {
		t.Fatal(err)
	}
	// Cancel on the third walk step (root, a.sav, b.sav): a.sav is seen, b.sav isn't.
	defer func(d time.Duration) { pauseEvery = d }(pauseEvery)
	pauseEvery = 0
	ctx, cancel := context.WithCancel(context.Background())
	steps := 0
	pause := func(context.Context) {
		if steps++; steps == 3 {
			cancel()
		}
	}
	_, _, _, _, _, _, _ = mirror(ctx, Folder{ID: id, Label: "C", Path: src}, Options{Target: target, Pause: pause}, "x", &Progress{})
	for _, n := range []string{"a.sav", "b.sav"} {
		if _, err := os.Stat(filepath.Join(target, id, n)); err != nil {
			t.Errorf("%s retired by a cancelled run", n)
		}
	}
}

func TestForget(t *testing.T) {
	target := t.TempDir()
	if err := Forget(target, "..", true); err == nil {
		t.Fatal("Forget accepted ..")
	}
	id := "forget-" + time.Now().Format("150405")
	write(t, filepath.Join(target, id, "a.sav"), "a")
	write(t, filepath.Join(target, VersionsDir, id, "x", "a.sav"), "a")
	write(t, filepath.Join(target, "other", "b.sav"), "b")
	if err := Forget(target, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, id)); err == nil {
		t.Error("backup not deleted")
	}
	if _, err := os.Stat(filepath.Join(target, VersionsDir, id)); err == nil {
		t.Error("versions not deleted")
	}
	if _, err := os.Stat(filepath.Join(target, "other", "b.sav")); err != nil {
		t.Error("unrelated backup deleted")
	}
}

func TestCopyHistory(t *testing.T) {
	target := t.TempDir()
	write(t, filepath.Join(target, "old", "a.sav"), "a")
	write(t, filepath.Join(target, VersionsDir, "old", "2026-01-02_030405", "a.sav"), "v0")
	write(t, filepath.Join(target, "new--pc", "b.sav"), "mine")
	write(t, filepath.Join(target, "old", "b.sav"), "theirs")
	if err := CopyHistory(context.Background(), target, "old", "new--pc"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(target, "new--pc", "a.sav")) != "a" || len(Points(target, "new--pc")) != 1 {
		t.Fatal("history not copied")
	}
	if read(t, filepath.Join(target, "new--pc", "b.sav")) != "mine" {
		t.Fatal("existing file overwritten")
	}
	if read(t, filepath.Join(target, "old", "a.sav")) != "a" {
		t.Fatal("source changed")
	}
	if err := CopyHistory(context.Background(), target, "missing", "x"); err != nil {
		t.Fatalf("missing source: %v", err)
	}
}

func TestLockBlocksBackup(t *testing.T) {
	u, err := Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer u()
	if _, err := Run(context.Background(), nil, Options{Target: t.TempDir()}); err != ErrBusy {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

// A backup of a Steam Cloud folder carries the backing-up PC's
// steam_autocloud.vdf; restoring must not put it on this PC.
func TestRestoreSkipsSteamMarker(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "marker-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	f := Folder{ID: id, Label: "M", Path: src}
	write(t, filepath.Join(target, id, "save.sav"), "x")
	write(t, filepath.Join(target, id, "steam_autocloud.vdf"), "other PC")
	n, err := Restore(target, f, time.Time{})
	if err != nil || n != 1 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(src, "steam_autocloud.vdf")); err == nil {
		t.Error("steam_autocloud.vdf restored")
	}
}

// A scoped folder (a game folder with deployed mods) backs up its listed
// files only; one that leaves the list retires into the history.
func TestRunScoped(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	id := "test-scoped-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	write(t, filepath.Join(src, "Skyrim.esm"), "the game's own")
	write(t, filepath.Join(src, "SkyUI_SE.esp"), "mod")
	write(t, filepath.Join(src, "meshes", "sky", "sky.nif"), "mod mesh")
	write(t, filepath.Join(src, "meshes", "vanilla.nif"), "the game's own")
	f := Folder{ID: id, Label: "Data", Path: src, Scoped: true, Only: []string{"SkyUI_SE.esp", "Meshes/Sky/sky.nif"}}

	if res, err := Run(context.Background(), []Folder{f}, Options{Target: target, KeepDays: 30}); err != nil || !res.OK {
		t.Fatalf("run: %v %v", err, res.Errors)
	}
	got := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(target, id), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(filepath.Join(target, id), p)
			got[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if len(got) != 2 || !got["SkyUI_SE.esp"] || !got["meshes/sky/sky.nif"] {
		t.Fatalf("backed up %v, want only the two mod files", got)
	}

	// The mod goes away: its backup copy moves into the history.
	time.Sleep(1100 * time.Millisecond)
	f.Only = []string{"meshes/sky/sky.nif"}
	if res, err := Run(context.Background(), []Folder{f}, Options{Target: target, KeepDays: 30}); err != nil || !res.OK {
		t.Fatalf("run: %v %v", err, res.Errors)
	}
	if _, err := os.Stat(filepath.Join(target, id, "SkyUI_SE.esp")); err == nil {
		t.Error("file that left the list still in the backup")
	}
	if len(Points(target, id)) == 0 {
		t.Error("no history for the file that left the list")
	}

	// Nothing listed (no update applied yet): nothing is backed up or retired.
	empty := Folder{ID: id + "-e", Label: "Data", Path: src, Scoped: true}
	defer os.Remove(indexPath(empty.ID))
	if _, err := Run(context.Background(), []Folder{empty}, Options{Target: target}); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(filepath.Join(target, empty.ID)); len(es) != 0 {
		t.Errorf("scoped folder with no files backed up %d entries", len(es))
	}

	// A restore point taken before syncing covers the listed files only.
	snapTarget := t.TempDir()
	n, err := Snapshot(context.Background(), snapTarget, f)
	if err != nil || n != 1 {
		t.Errorf("snapshot saved %d files (%v), want 1", n, err)
	}
}

// Two files kept under the same name in the same second both stay.
func TestKeepNeverOverwrites(t *testing.T) {
	target, src := t.TempDir(), t.TempDir()
	for _, s := range []string{"one", "two", "three"} {
		p := filepath.Join(src, "save.dat")
		write(t, p, s)
		if _, err := Keep(target, "g", p, "save.dat", true); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, pt := range Points(target, "g") {
		b, err := os.ReadFile(filepath.Join(target, VersionsDir, "g", pt.Format(stampFmt), "save.dat"))
		if err == nil {
			got[string(b)] = true
		}
	}
	if len(got) != 3 {
		t.Errorf("kept %v, want all three", got)
	}
}
