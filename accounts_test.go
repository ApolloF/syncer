package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
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
