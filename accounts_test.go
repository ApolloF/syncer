package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func TestCopyHistoriesFinishesWhatASplitLeft(t *testing.T) {
	t.Cleanup(paths.SetRootForTest(paths.Roaming, t.TempDir()))
	target := t.TempDir()
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.BackupRoot = target }); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(target, "bg3", "Story", "save.lsv"), "save", time.Now().Add(-time.Hour))
	point := filepath.Join(backup.VersionsDir, "bg3", "2026-01-02_030405", "Story", "old.lsv")
	writeAt(t, filepath.Join(target, point), "old", time.Now().Add(-2*time.Hour))
	store.UpdateState(func(st *store.State) {
		st.HistoryCopies = map[string]string{"bg3--alice": "bg3", "bg3--bob": "bg3"}
	})
	copyHistories(context.Background())
	for _, id := range []string{"bg3--alice", "bg3--bob"} {
		p := filepath.Join(target, backup.VersionsDir, id, "2026-01-02_030405", "Story", "old.lsv")
		if b, err := os.ReadFile(p); err != nil || string(b) != "old" {
			t.Errorf("%s restore point: %q, %v", id, b, err)
		}
		// The backup copy is taken over by the account's own backup.
		if _, err := os.Stat(filepath.Join(target, id)); err == nil {
			t.Errorf("%s: game's backup copied", id)
		}
	}
	if left := store.LoadState().HistoryCopies; len(left) != 0 {
		t.Errorf("still to copy: %v", left)
	}
}

// An account folder's backup knows which backup to take files over from,
// until that backup is gone.
func TestBackupFoldersKnowWhereTheyCameFrom(t *testing.T) {
	t.Cleanup(paths.SetRootForTest(paths.Roaming, t.TempDir()))
	target := t.TempDir()
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.SyncDisabled = true
		s.BackupOnly = map[string]store.LocalFolder{"bg3--alice": {ID: "bg3--alice", Label: "BG3 (Alice)", Path: t.TempDir()}}
	}); err != nil {
		t.Fatal(err)
	}
	store.UpdateState(func(st *store.State) { st.BackupFrom = map[string]string{"bg3--alice": "bg3"} })
	if err := os.MkdirAll(filepath.Join(target, "bg3"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := backupFolders()
	if err != nil || len(fs) != 1 || fs[0].From != "bg3" {
		t.Fatalf("folders = %+v, %v", fs, err)
	}
	forgetGoneBackupFrom(target)
	if store.LoadState().BackupFrom["bg3--alice"] != "bg3" {
		t.Fatal("forgot a backup that is still there")
	}
	if err := os.Remove(filepath.Join(target, "bg3")); err != nil {
		t.Fatal(err)
	}
	forgetGoneBackupFrom(target)
	if len(store.LoadState().BackupFrom) != 0 {
		t.Error("still taking files over from a backup that is gone")
	}
}

func TestMarkedFolderIsNotSavedAgain(t *testing.T) {
	t.Cleanup(paths.SetRootForTest(paths.Roaming, t.TempDir()))
	t.Cleanup(paths.SetRootForTest(paths.Local, t.TempDir()))
	target, dir := t.TempDir(), t.TempDir()
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.BackupRoot = target }); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(dir, "save.lsv"), "save", time.Now().Add(-time.Hour))
	markProtected("bg3--bob", dir, time.Now())
	if err := protectExisting(context.Background(), "bg3--bob", "BG3 (Bob)", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "bg3--bob")); err == nil {
		t.Error("saved again although the split's restore point covers it")
	}
	// Changed since: saved after all.
	writeAt(t, filepath.Join(dir, "save.lsv"), "played on", time.Now().Add(time.Minute))
	if err := protectExisting(context.Background(), "bg3--bob", "BG3 (Bob)", dir); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(target); len(es) == 0 {
		t.Error("a changed folder wasn't saved")
	}
}

// Each account sees only its own part of a launcher's data, by the subfolder
// the launcher names after it; "shared" belongs to no account.
func TestLauncherSaveListsOnlyTheAccountsPart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeAt(t, filepath.Join(dir, "acc1", "desk.json"), "a", now)
	writeAt(t, filepath.Join(dir, "acc1", "tv.json"), "ab", now.Add(-time.Hour))
	writeAt(t, filepath.Join(dir, "acc2", "desk.json"), "abc", now)
	writeAt(t, filepath.Join(dir, "shared", "desk.json"), "abcd", now)
	f := syncthing.Folder{ID: "seaglass", Label: "Seaglass (playtime, achievements, settings)", Path: dir}

	s, ok := launcherSave(f, "Seaglass", "acc1", true)
	if !ok || s.Launcher != "Seaglass" || s.FolderID != "seaglass" || !s.Here || !s.Synced || s.Path != filepath.Join(dir, "acc1") {
		t.Fatalf("acc1 = %+v, %v", s, ok)
	}
	if len(s.Files) != 2 || s.Files[0].Rel != "desk.json" || s.Files[1].Rel != "tv.json" || s.Bytes != 3 {
		t.Errorf("acc1 files = %+v (%d bytes)", s.Files, s.Bytes)
	}
	if s, ok := launcherSave(f, "Seaglass", "acc2", false); !ok || s.Here || len(s.Files) != 1 || s.Bytes != 3 {
		t.Errorf("acc2 = %+v, %v", s, ok)
	}
	if _, ok := launcherSave(f, "Seaglass", "acc3", false); ok {
		t.Error("an account without a subfolder got a row")
	}
}

// Splitting a save folder this PC reaches through a junction says so,
// instead of that the folder can't be split.
func TestSplittableLinkSaysSo(t *testing.T) {
	base := t.TempDir()
	roaming := filepath.Join(base, "roaming")
	moved := filepath.Join(base, "other drive", "Game")
	for _, d := range []string{roaming, moved} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(paths.SetRootForTest(paths.Roaming, roaming))
	t.Cleanup(paths.SetRootForTest(paths.Local, filepath.Join(base, "local")))
	plain := filepath.Join(roaming, "Plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if root, rel, err := splittable(plain); err != nil || root != paths.Roaming || rel != "Plain" {
		t.Errorf("splittable(plain) = %q, %q, %v", root, rel, err)
	}
	link := filepath.Join(roaming, "Game")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, moved).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction: %v %s", err, out)
	}
	if _, _, err := splittable(link); !errors.Is(err, accounts.ErrSplitLink) {
		t.Errorf("splittable(link) = %v, want %v", err, accounts.ErrSplitLink)
	}
}
