package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	store.UpdateState(func(st *store.State) {
		st.HistoryCopies = map[string]string{"bg3--alice": "bg3", "bg3--bob": "bg3"}
	})
	copyHistories(context.Background())
	for _, id := range []string{"bg3--alice", "bg3--bob"} {
		if b, err := os.ReadFile(filepath.Join(target, id, "Story", "save.lsv")); err != nil || string(b) != "save" {
			t.Errorf("%s: %q, %v", id, b, err)
		}
	}
	if left := store.LoadState().HistoryCopies; len(left) != 0 {
		t.Errorf("still to copy: %v", left)
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
