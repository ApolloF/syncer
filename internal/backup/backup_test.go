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
		".stfolder":              true,
		"Trainers":               true,
		`Trainers\x\y.exe`:       true,
		"root-only.txt":          true,
		"sub/root-only.txt":      false,
		"a/b/debug.LOG":          true,
		"cache/x":                true,
		"saves/slot1.sav":        false,
		"~syncthing~foo.tmp":     true,
		"x/.syncthing.a.sav.tmp": true,
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
