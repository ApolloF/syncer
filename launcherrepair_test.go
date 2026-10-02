package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func writeAt(t *testing.T, p, s string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestRestorePlanAfterTheFolderWasEmptied(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	const id = "seaglass-profile"
	wiped := time.Date(2026, 10, 2, 15, 0, 0, 0, time.Local)
	before, after := wiped.Add(-time.Hour), wiped.Add(time.Hour)
	mirror := filepath.Join(target, id)
	point := filepath.Join(target, backup.VersionsDir, id, backup.Stamp(after.Add(time.Minute)))

	// Gone here: restored as it was.
	writeAt(t, filepath.Join(mirror, "snig77", "PC-Other-2b.json"), "other pc", before)
	// Started again by the launcher and backed up since: the copy from
	// before (kept in a restore point) goes next to it, the new one doesn't.
	writeAt(t, filepath.Join(mirror, "snig77", "PC-Florian-1e.json"), "new start", after)
	writeAt(t, filepath.Join(point, "snig77", "PC-Florian-1e.json"), "old playtime", before)
	writeAt(t, filepath.Join(dir, "snig77", "PC-Florian-1e.json"), "new start, more", after.Add(time.Minute))
	// Started again, but holds what the backup has: nothing to do.
	writeAt(t, filepath.Join(mirror, "settings.json"), "same", before)
	writeAt(t, filepath.Join(dir, "settings.json"), "same", after)
	// Never emptied (created before every copy): left alone.
	writeAt(t, filepath.Join(mirror, "kept.json"), "older", before)
	writeAt(t, filepath.Join(dir, "kept.json"), "changed here", after)
	// Brought back next to it by an earlier repair already.
	writeAt(t, filepath.Join(mirror, "twice.json"), "old", before)
	writeAt(t, filepath.Join(dir, "twice.json"), "new", after)
	writeAt(t, filepath.Join(dir, restoredName("twice.json", before)), "old", before)
	// Syncthing's and Syncer's own files never come back.
	writeAt(t, filepath.Join(mirror, ".stignore"), "x", before)
	writeAt(t, filepath.Join(mirror, ".stfolder", "syncthing-folder-1.txt"), "x", before)
	writeAt(t, filepath.Join(mirror, ".stversions", "a.json"), "x", before)
	writeAt(t, filepath.Join(mirror, "b.json.syncer-tmp"), "x", before)

	born := map[string]time.Time{
		filepath.Join(dir, "snig77", "PC-Florian-1e.json"): after.Add(-time.Minute),
		filepath.Join(dir, "settings.json"):                after,
		filepath.Join(dir, "kept.json"):                    before.Add(-24 * time.Hour),
		filepath.Join(dir, "twice.json"):                   after,
	}
	created := func(p string) (time.Time, bool) { t, ok := born[p]; return t, ok }
	steps := planRestore(dir, backup.Copies(target, id), backup.LoadMatcher(dir).Ignored, created)

	want := map[string]struct {
		content string
		beside  bool
	}{
		filepath.Join("snig77", "PC-Other-2b.json"):                            {"other pc", false},
		filepath.Join("snig77", "PC-Florian-1e.restored-20261002-140000.json"): {"old playtime", true},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %+v, want %d", steps, len(want))
	}
	for _, st := range steps {
		w, ok := want[st.To]
		if !ok {
			t.Errorf("unexpected step to %s", st.To)
			continue
		}
		b, err := os.ReadFile(st.From.Path)
		if err != nil || string(b) != w.content || st.Beside != w.beside {
			t.Errorf("%s: from %q (beside %v), want %q (beside %v)", st.To, b, st.Beside, w.content, w.beside)
		}
	}
}

func TestRestorePlanLeavesFilesWithUnknownCreationAlone(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	old := time.Now().Add(-time.Hour)
	writeAt(t, filepath.Join(target, "f", "a.json"), "backup", old)
	writeAt(t, filepath.Join(dir, "a.json"), "here", time.Now())
	none := func(string) (time.Time, bool) { return time.Time{}, false }
	if steps := planRestore(dir, backup.Copies(target, "f"), backup.LoadMatcher(dir).Ignored, none); len(steps) != 0 {
		t.Errorf("steps = %+v", steps)
	}
}

func TestSecondCopyInARestorePointCountsAsTheSameFile(t *testing.T) {
	target := t.TempDir()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	point := filepath.Join(target, backup.VersionsDir, "f", backup.Stamp(at))
	writeAt(t, filepath.Join(point, "a.json"), "1", at.Add(-2*time.Hour))
	writeAt(t, filepath.Join(point, "a.json.syncer-kept-123"), "2", at.Add(-time.Hour))
	cs := backup.Copies(target, "f")["a.json"]
	if len(cs) != 2 || cs[0].Rel != "a.json" || cs[1].Rel != "a.json" || !cs[0].Modified.After(cs[1].Modified) {
		t.Fatalf("copies = %+v", cs)
	}
	if !cs[0].Point.Equal(at) {
		t.Errorf("point = %v, want %v", cs[0].Point, at)
	}
}

func TestRestoredCopyNeverReplacesAFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "backup.json"), filepath.Join(dir, "sub", "a.json")
	mod := time.Date(2026, 10, 2, 15, 57, 0, 0, time.Local)
	writeAt(t, src, "old", mod)
	if err := putCopy(src, dst, mod); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dst); err != nil || !fi.ModTime().Equal(mod) {
		t.Fatalf("restored: %v, %v", fi, err)
	}
	writeAt(t, src, "other", mod)
	if err := putCopy(src, dst, mod); err == nil {
		t.Error("replaced an existing file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Errorf("content = %q", b)
	}
	if _, err := os.Stat(dst + ".syncer-tmp"); err == nil {
		t.Error("temporary file left behind")
	}
}

func TestRestoredName(t *testing.T) {
	at := time.Date(2026, 10, 2, 15, 57, 0, 0, time.Local)
	if got := restoredName(filepath.Join("snig77", "PC-Florian-1ef0a2c9.json"), at); got != filepath.Join("snig77", "PC-Florian-1ef0a2c9.restored-20261002-155700.json") {
		t.Errorf("got %q", got)
	}
	if got := restoredName("settings", at); got != "settings.restored-20261002-155700" {
		t.Errorf("no extension: %q", got)
	}
}

func TestOnlyAMissingMarkerOrFolderNeedsRepair(t *testing.T) {
	launcherDir := t.TempDir()
	here := syncthing.Folder{Path: filepath.Join(launcherDir, "Profile")}
	gone := syncthing.Folder{Path: filepath.Join(launcherDir, "Missing", "Profile")}
	for _, c := range []struct {
		f    syncthing.Folder
		err  string
		want bool
	}{
		{here, "folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)", true},
		{here, "folder path missing", true},
		{gone, "folder path missing", false}, // the launcher's own folder is gone too
		{here, "folder path not a directory", false},
		{here, "", false},
	} {
		if got := needsRepair(c.f, syncthing.FolderStatus{State: "error", Error: c.err}); got != c.want {
			t.Errorf("%s, %q: %v, want %v", c.f.Path, c.err, got, c.want)
		}
	}
}

func TestFileCreatedIsKnownOnWindows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.json")
	writeAt(t, p, "x", time.Now().Add(-48*time.Hour))
	at, ok := fileCreated(p)
	if !ok || time.Since(at) > time.Hour {
		t.Errorf("created %v (%v), want just now", at, ok)
	}
}
