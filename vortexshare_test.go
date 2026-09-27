package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"

	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

func vortexState(t *testing.T, kv map[string]string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SYNCER_VORTEX_ROOT", root)
	old := mods.VortexStateBackupRoot
	mods.VortexStateBackupRoot = func() string { return filepath.Join(root, "backups") }
	t.Cleanup(func() { mods.VortexStateBackupRoot = old })
	t.Cleanup(paths.SetRootForTest(paths.Roaming, t.TempDir())) // Syncer's own state
	putVortex(t, kv, nil)
}

func putVortex(t *testing.T, put map[string]string, del []string) {
	t.Helper()
	db, err := leveldb.OpenFile(mods.VortexDBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for k, v := range put {
		_ = db.Put([]byte(k), []byte(v), nil)
	}
	for _, k := range del {
		_ = db.Delete([]byte(k), nil)
	}
}

func modRec(folder, version string, enabled bool) *mods.VortexMod {
	return &mods.VortexMod{Enabled: enabled, Leaves: map[string]json.RawMessage{
		"state": json.RawMessage(`"installed"`), "installationPath": json.RawMessage(`"` + folder + `"`),
		"attributes###version": json.RawMessage(`"` + version + `"`),
	}}
}

// runShare runs one round and returns how many mods it changed in Vortex.
func runShare(t *testing.T, staging string, lists map[string]mods.ShareList, complete bool) (vortexShareState, int) {
	t.Helper()
	for dev, l := range lists {
		lists[dev] = l.Valid()
	}
	before := loadVortexShare().Status["skyrimse"].Applied
	st, _, err := shareAll(map[string]string{"skyrimse": staging}, lists, func(string) bool { return complete })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(vortexSharePath(), st); err != nil {
		t.Fatal(err)
	}
	s := st.Status["skyrimse"]
	if s.Err != "" {
		t.Fatal(s.Err)
	}
	if s.Applied.Equal(before) {
		return st, 0
	}
	return st, s.AppliedN
}

func TestShareVortexList(t *testing.T) {
	vortexState(t, map[string]string{
		"settings###profiles###lastActiveProfile###skyrimse":      `"p1"`,
		"persistent###profiles###p1###gameId":                     `"skyrimse"`,
		"persistent###mods###skyrimse###SkyUI###state":            `"installed"`,
		"persistent###mods###skyrimse###SkyUI###installationPath": `"SkyUI"`,
		"persistent###profiles###p1###modState###SkyUI###enabled": `true`,
		"persistent###mods###skyrimse###Old###state":              `"installed"`,
		"persistent###mods###skyrimse###Old###installationPath":   `"Old"`,
	})
	staging := t.TempDir()
	for _, d := range []string{"SkyUI", "USSEP"} {
		_ = os.Mkdir(filepath.Join(staging, d), 0o755)
	}
	peer := mods.ShareList{"skyrimse": {
		"SkyUI": {At: 1, Mod: modRec("SkyUI", "1.0", false)}, // as the other PC had it when sharing started
		"USSEP": {At: 100, Mod: modRec("USSEP", "4.3", true)},
		"Old":   {At: 200, Gone: true},
		"New":   {At: 300, Mod: modRec("New", "1", true)}, // its folder hasn't arrived yet
	}}
	st, n := runShare(t, staging, map[string]mods.ShareList{"B": peer}, true)
	if n != 2 {
		t.Fatalf("first look changed %d mods, want 2 (USSEP added, Old removed)", n)
	}
	if st.Status["skyrimse"].Waiting != 1 {
		t.Errorf("waiting %d, want 1 (New)", st.Status["skyrimse"].Waiting)
	}
	db, _ := mods.OpenVortexDB()
	gm, _ := db.Mods("skyrimse")
	db.Close()
	if !gm.Mods["SkyUI"].Enabled {
		t.Error("what this PC had when sharing started must not be undone by another PC's equally old state")
	}
	if u := gm.Mods["USSEP"]; !u.Enabled || string(u.Leaves["attributes###version"]) != `"4.3"` {
		t.Errorf("USSEP %+v", u)
	}
	if _, ok := gm.Mods["Old"]; ok {
		t.Error("a mod removed on the other PC, whose folder is gone here, should leave Vortex")
	}
	if matches, _ := filepath.Glob(filepath.Join(mods.VortexStateBackupRoot(), "*", "CURRENT")); len(matches) != 1 {
		t.Errorf("want one copy of Vortex's database before the change, got %v", matches)
	}

	// Nothing changed: nothing to do, and the list stays as it is.
	before := st.List["skyrimse"]["USSEP"]
	if st, n = runShare(t, staging, map[string]mods.ShareList{"B": peer}, true); n != 0 {
		t.Fatalf("second look changed %d mods", n)
	}
	if st.List["skyrimse"]["USSEP"].At != before.At {
		t.Error("a mod written from another PC must not look changed here afterwards")
	}

	// Disabled here in Vortex: this PC's version is now the newest.
	putVortex(t, map[string]string{"persistent###profiles###p1###modState###USSEP###enabled": `false`}, nil)
	st, _ = runShare(t, staging, map[string]mods.ShareList{"B": peer}, true)
	if e := st.List["skyrimse"]["USSEP"]; e.At <= 100 || e.Mod == nil || e.Mod.Enabled {
		t.Errorf("local change not noted: %+v", e)
	}

	// Removed here in Vortex: noted as gone.
	putVortex(t, nil, []string{"persistent###mods###skyrimse###SkyUI###state", "persistent###mods###skyrimse###SkyUI###installationPath"})
	st, _ = runShare(t, staging, map[string]mods.ShareList{"B": peer}, true)
	if e := st.List["skyrimse"]["SkyUI"]; !e.Gone {
		t.Errorf("removal not noted: %+v", e)
	}

	// The folder of New arrives.
	_ = os.Mkdir(filepath.Join(staging, "New"), 0o755)
	if st, n = runShare(t, staging, map[string]mods.ShareList{"B": peer}, true); n != 1 || st.Status["skyrimse"].Waiting != 0 {
		t.Errorf("New: %d changed, %d waiting", n, st.Status["skyrimse"].Waiting)
	}
}

func TestShareVortexWaitsForFiles(t *testing.T) {
	vortexState(t, map[string]string{
		"settings###profiles###lastActiveProfile###skyrimse": `"p1"`,
		"persistent###profiles###p1###gameId":                `"skyrimse"`,
	})
	staging := t.TempDir()
	_ = os.Mkdir(filepath.Join(staging, "USSEP"), 0o755)
	lists := map[string]mods.ShareList{"B": {"skyrimse": {"USSEP": {At: 100, Mod: modRec("USSEP", "4.3", true)}}}}
	st, n := runShare(t, staging, lists, false)
	if n != 0 || st.Status["skyrimse"].Waiting != 1 {
		t.Fatalf("while files still arrive: %d changed, %d waiting", n, st.Status["skyrimse"].Waiting)
	}
	if matches, _ := filepath.Glob(filepath.Join(mods.VortexStateBackupRoot(), "*")); len(matches) != 0 {
		t.Errorf("nothing to write, yet a copy of Vortex's database was made: %v", matches)
	}
}
