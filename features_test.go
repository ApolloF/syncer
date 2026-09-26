package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

func TestWantAutoOnlyInstalled(t *testing.T) {
	g := discover.Found{Name: "Game", Path: `C:\Saves\Game`, Known: true, Files: 3, Size: 10}
	s := store.Settings{AutoAddMaxGB: 1, Dismissed: map[string]bool{}, Ignored: map[string]bool{}}
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	if !wantAuto(g, s, no) {
		t.Error("\"only installed\" off: a game that isn't installed should still be added")
	}
	s.InstalledOnly = true
	if wantAuto(g, s, no) {
		t.Error("\"only installed\" on: saves of a game that isn't installed were added (it keeps coming back)")
	}
	if !wantAuto(g, s, yes) {
		t.Error("\"only installed\" on: an installed game wasn't added")
	}
	s.Dismissed[dismissKey(g.Path)] = true
	if wantAuto(g, s, yes) {
		t.Error("dismissed game added")
	}
}

func TestSameName(t *testing.T) {
	if !sameName("  hollow knight ", "Hollow Knight") || sameName("Hollow", "Hollow Knight") || sameName("", " ") {
		t.Error("sameName")
	}
}

// testDir makes a folder where saves may live (temp folders may not: they're
// under AppData\Local\Temp), removed after the test.
func testDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(paths.Root(paths.Local), "syncer-test-"+time.Now().Format("150405.000000"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDeletableDir(t *testing.T) {
	base := testDir(t)
	game := filepath.Join(base, "Game")
	if err := os.MkdirAll(filepath.Join(game, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file.sav")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		path   string
		others []string
		target string
		ok     bool
	}{
		{"a save folder", game, []string{filepath.Join(base, "Other")}, `G:\My Drive\GameSaveBackup`, true},
		{"relative", "Game", nil, "", false},
		{"missing", filepath.Join(base, "Nope"), nil, "", false},
		{"a file", file, nil, "", false},
		{"holds another game", game, []string{filepath.Join(game, "sub")}, "", false},
		{"inside another game", filepath.Join(game, "sub"), []string{game}, "", false},
		{"holds the backup", game, nil, filepath.Join(game, "sub"), false},
		{"Documents itself", paths.Root(paths.Documents), nil, "", false},
		{"the profile", paths.Root(paths.Home), nil, "", false},
		{"Syncer's own data", paths.AppDir(), nil, "", false},
	} {
		if err := deletableDir(tt.path, tt.others, tt.target); (err == nil) != tt.ok {
			t.Errorf("%s: deletableDir(%q) = %v, want ok=%v", tt.name, tt.path, err, tt.ok)
		}
	}
}

func TestCleanExclusions(t *testing.T) {
	got, err := cleanExclusions([]string{" *.log ", "", "Screenshots", "*.LOG", "(?i)*shadercache*"})
	if err != nil || !slices.Equal(got, []string{"*.log", "Screenshots", "(?i)*shadercache*"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"*", "**", "/", "/**", "(?i)*", "#include x", "!keep", "// c", "a\nb", strings.Repeat("x", 201)} {
		if _, err := cleanExclusions([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := make([]string, maxExclusions+1)
	for i := range many {
		many[i] = "f" + strings.Repeat("x", i)
	}
	if _, err := cleanExclusions(many); err == nil {
		t.Error("too many patterns accepted")
	}
}

func TestMergeIgnores(t *testing.T) {
	user := []string{"// mine", "Trainers/", ""}
	got := mergeIgnores(user, []string{"*.log"})
	want := []string{"// mine", "Trainers/", backup.IgnoreBegin, "*.log", backup.IgnoreEnd}
	if !slices.Equal(got, want) {
		t.Fatalf("add block: %q", got)
	}
	if again := mergeIgnores(got, []string{"*.dmp"}); !slices.Equal(again, []string{"// mine", "Trainers/", backup.IgnoreBegin, "*.dmp", backup.IgnoreEnd}) {
		t.Errorf("replace block: %q", again)
	}
	if none := mergeIgnores(got, nil); !slices.Equal(none, []string{"// mine", "Trainers/"}) {
		t.Errorf("remove block: %q", none)
	}
	if empty := mergeIgnores(nil, nil); len(empty) != 0 {
		t.Errorf("nothing to nothing: %q", empty)
	}
}

func TestOwnBackupID(t *testing.T) {
	id := backupOnlyID("Hollow Knight", nil)
	id2 := backupOnlyID("Hollow Knight", map[string]bool{id: true})
	for _, x := range []string{id, id2} {
		if !ownBackupID(x) {
			t.Errorf("%q not recognized as this PC's", x)
		}
	}
	for _, x := range []string{"hollow-knight", "hollow-knight--other-pc", "hollow-knight--" + hostSuffix() + "x", "hollow-knight--" + hostSuffix() + "-x"} {
		if ownBackupID(x) {
			t.Errorf("%q taken for this PC's", x)
		}
	}
}

func TestRecordFolders(t *testing.T) {
	now := time.Now()
	st := store.State{FolderBackups: map[string]time.Time{"old": now.AddDate(-2, 0, 0), "kept": now.Add(-time.Hour)}}
	recordFolders(&st, &store.BackupRun{Finished: now, Backed: []string{"a"}})
	if !st.FolderBackups["a"].Equal(now) || st.FolderBackups["kept"].IsZero() {
		t.Errorf("recorded: %v", st.FolderBackups)
	}
	if _, ok := st.FolderBackups["old"]; ok {
		t.Error("year-old entry not pruned")
	}
}

// A Steam emulator's copy of saves the game keeps in its own folder (Baldur's
// Gate 3 on RUNE) isn't synced on its own; an emulator folder that is the
// game's only save is.
func TestWantAutoSkipsEmulatorCopies(t *testing.T) {
	s := store.Settings{AutoAddMaxGB: 1, Dismissed: map[string]bool{}, Ignored: map[string]bool{}}
	yes := func(string) bool { return true }
	g := discover.Found{Name: "Baldur's Gate 3 (RUNE saves)", Path: `C:\Users\Public\Documents\Steam\RUNE\1086940`,
		Emulator: "RUNE", Known: true, Files: 3, Size: 10}
	if !wantAuto(g, s, yes) {
		t.Error("emulator saves with no other copy weren't added")
	}
	g.CopyOf = "Baldur's Gate 3"
	if wantAuto(g, s, yes) {
		t.Error("copy of the game's own saves added")
	}
}

func TestWithSyncIgnores(t *testing.T) {
	if got := withSyncIgnores(nil); !slices.Equal(got, []string{"steam_autocloud.vdf"}) {
		t.Errorf("no exclusions: %v", got)
	}
	if got := withSyncIgnores([]string{"*.log", "STEAM_AUTOCLOUD.VDF"}); !slices.Equal(got, []string{"steam_autocloud.vdf", "*.log"}) {
		t.Errorf("with exclusions: %v", got)
	}
}
