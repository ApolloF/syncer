package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

func TestMergeFolders(t *testing.T) {
	synced := []backup.Folder{{ID: "a", Label: "A", Path: `C:\Saves\A`}}
	bo := map[string]store.LocalFolder{
		"a":     {ID: "a", Label: "A dup id", Path: `C:\Other`},
		"a2":    {ID: "a2", Label: "A dup path", Path: `c:\saves\a\`},
		"b--pc": {ID: "b--pc", Label: "B", Path: `C:\Saves\B`},
	}
	got := mergeFolders(synced, bo)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b--pc" {
		t.Fatalf("got %+v", got)
	}
}

func TestBackupOnlyID(t *testing.T) {
	id := backupOnlyID("Hollow Knight", nil)
	if !strings.HasPrefix(id, "hollow-knight--") || !paths.ValidID(id) {
		t.Fatalf("id %q", id)
	}
	if id2 := backupOnlyID("Hollow Knight", map[string]bool{id: true}); id2 == id || !paths.ValidID(id2) {
		t.Fatalf("collision not avoided: %q", id2)
	}
	if long := backupOnlyID(strings.Repeat("x", 200), nil); !paths.ValidID(long) {
		t.Fatalf("long label gives invalid id %q", long)
	}
}

func TestCleanMarkers(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, ".stfolder"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".stfolder", "syncthing-folder-abc.txt"), nil, 0o644))
	must(os.MkdirAll(filepath.Join(dir, ".stversions"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "save.sav"), []byte("x"), 0o644))
	if v := cleanMarkers(dir); v != "" {
		t.Errorf("empty .stversions reported as kept: %q", v)
	}
	for _, n := range []string{".stfolder", ".stversions"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s not removed", n)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, ".stversions"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".stversions", "old.sav"), []byte("o"), 0o644))
	if v := cleanMarkers(dir); v == "" {
		t.Error("non-empty .stversions not reported")
	}
	for _, n := range []string{"save.sav", filepath.Join(".stversions", "old.sav")} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was deleted", n)
		}
	}
}
