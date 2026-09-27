package mods

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

// fakeVortexDB makes a Vortex state database holding kv (keys joined by
// "###", values as JSON) and points the package at it.
func fakeVortexDB(t *testing.T, kv map[string]string) string {
	t.Helper()
	root := testDir(t)
	t.Setenv("SYNCER_VORTEX_ROOT", root)
	db, err := leveldb.OpenFile(filepath.Join(root, "state.v2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if err := db.Put([]byte(k), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func dbGet(t *testing.T, k string) (string, bool) {
	t.Helper()
	db, err := leveldb.OpenFile(VortexDBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.Get([]byte(k), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(v), true
}

var baseState = map[string]string{
	"settings###profiles###lastActiveProfile###skyrimse":          `"p1"`,
	"persistent###profiles###p1###gameId":                         `"skyrimse"`,
	"persistent###profiles###p2###gameId":                         `"skyrimse"`,
	"persistent###profiles###p3###gameId":                         `"fallout4"`,
	"persistent###mods###skyrimse###SkyUI###state":                `"installed"`,
	"persistent###mods###skyrimse###SkyUI###installationPath":     `"SkyUI"`,
	"persistent###mods###skyrimse###SkyUI###archiveId":            `"local-archive"`,
	"persistent###mods###skyrimse###SkyUI###attributes###version": `"5.2"`,
	"persistent###mods###skyrimse###SkyUI###rules":                `[{"type":"after","reference":{"id":"SKSE"}}]`,
	"persistent###profiles###p1###modState###SkyUI###enabled":     `true`,
	"persistent###profiles###p2###modState###SkyUI###enabled":     `false`,
	"persistent###mods###skyrimse###Half.Done###state":            `"installing"`,
	"persistent###mods###fallout4###Other###state":                `"installed"`,
	"settings###interface###language":                             `"en"`,
}

func TestVortexDBReadsMods(t *testing.T) {
	fakeVortexDB(t, baseState)
	db, err := OpenVortexDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gm, err := db.Mods("skyrimse")
	if err != nil {
		t.Fatal(err)
	}
	if gm.Profile != "p1" {
		t.Fatalf("profile %q", gm.Profile)
	}
	if len(gm.Mods) != 2 {
		t.Fatalf("mods %v", gm.Mods)
	}
	sky := gm.Mods["SkyUI"]
	if !sky.Enabled || !sky.Installed() || sky.Folder("SkyUI") != "SkyUI" {
		t.Fatalf("SkyUI %+v", sky)
	}
	if _, ok := sky.Leaves["archiveId"]; ok {
		t.Fatal("the archive id is this PC's own and must not be shared")
	}
	if string(sky.Leaves["attributes###version"]) != `"5.2"` {
		t.Fatalf("leaves %v", sky.Leaves)
	}
	if gm.Mods["Half.Done"].Installed() {
		t.Fatal("a mod being installed isn't installed")
	}
}

func TestVortexDBOpenWhileLocked(t *testing.T) {
	fakeVortexDB(t, baseState)
	held, err := leveldb.OpenFile(VortexDBPath(), nil) // Vortex, running
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := OpenVortexDB(); !errors.Is(err, ErrVortexOpen) {
		t.Fatalf("want ErrVortexOpen, got %v", err)
	}
}

func TestVortexDBApply(t *testing.T) {
	fakeVortexDB(t, baseState)
	db, err := OpenVortexDB()
	if err != nil {
		t.Fatal(err)
	}
	newSky := VortexMod{Enabled: false, Leaves: map[string]json.RawMessage{
		"state": json.RawMessage(`"installed"`), "installationPath": json.RawMessage(`"SkyUI"`),
		"attributes###version": json.RawMessage(`"5.3"`),
	}}
	added := VortexMod{Enabled: true, Leaves: map[string]json.RawMessage{
		"state": json.RawMessage(`"installed"`), "installationPath": json.RawMessage(`"USSEP"`),
		"attributes###name": json.RawMessage(`"Unofficial Patch"`),
	}}
	err = db.Apply("skyrimse", "p1", []VortexChange{{ID: "SkyUI", Mod: &newSky}, {ID: "USSEP", Mod: &added}})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	want := map[string]string{
		"persistent###mods###skyrimse###SkyUI###attributes###version": `"5.3"`,
		"persistent###mods###skyrimse###SkyUI###archiveId":            `"local-archive"`, // kept: this PC's own
		"persistent###profiles###p1###modState###SkyUI###enabled":     `false`,
		"persistent###profiles###p1###modState###USSEP###enabled":     `true`,
		"persistent###mods###skyrimse###USSEP###attributes###name":    `"Unofficial Patch"`,
		"settings###interface###language":                             `"en"`,
	}
	for k, v := range want {
		if got, ok := dbGet(t, k); !ok || got != v {
			t.Errorf("%s = %q (%v), want %q", k, got, ok, v)
		}
	}
	if _, ok := dbGet(t, "persistent###mods###skyrimse###SkyUI###rules"); ok {
		t.Error("a part the new record doesn't have should be gone")
	}

	db, _ = OpenVortexDB()
	if err := db.Apply("skyrimse", "p1", []VortexChange{{ID: "SkyUI"}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, k := range []string{"persistent###mods###skyrimse###SkyUI###state", "persistent###mods###skyrimse###SkyUI###archiveId",
		"persistent###profiles###p1###modState###SkyUI###enabled", "persistent###profiles###p2###modState###SkyUI###enabled"} {
		if _, ok := dbGet(t, k); ok {
			t.Errorf("%s should be gone with the mod", k)
		}
	}
	if _, ok := dbGet(t, "persistent###mods###fallout4###Other###state"); !ok {
		t.Error("another game's mod was touched")
	}
}

func TestVortexModCheck(t *testing.T) {
	ok := VortexMod{Leaves: map[string]json.RawMessage{"state": json.RawMessage(`"installed"`)}}
	if err := ok.Check("SkyUI 5.2"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "..", `..\..\Windows`, "a/b", "C:x", "NUL", "x###y", "trailing.", "__vortex_staging_folder"} {
		if ok.Check(id) == nil {
			t.Errorf("mod id %q accepted", id)
		}
	}
	for _, p := range []string{`"..\\..\\Windows"`, `"C:\\Games"`, `"a/b"`, `".."`} {
		m := VortexMod{Leaves: map[string]json.RawMessage{"installationPath": json.RawMessage(p)}}
		if m.Check("x") == nil {
			t.Errorf("folder %s accepted", p)
		}
	}
	bad := VortexMod{Leaves: map[string]json.RawMessage{"archiveId": json.RawMessage(`"x"`)}}
	if bad.Check("x") == nil {
		t.Error("a PC's own archive id accepted from another PC")
	}
	notJSON := VortexMod{Leaves: map[string]json.RawMessage{"state": json.RawMessage(`{`)}}
	if notJSON.Check("x") == nil {
		t.Error("broken JSON accepted")
	}
}

func TestShareListValid(t *testing.T) {
	m := &VortexMod{Leaves: map[string]json.RawMessage{"state": json.RawMessage(`"installed"`)}}
	l := ShareList{
		"skyrimse": {"ok": {At: 5, Mod: m, Hash: "forged"}, "gone": {At: 6, Gone: true, Mod: m},
			"future": {At: 1 << 62, Mod: m}, "..": {At: 5, Mod: m}, "nomod": {At: 5}},
		"../evil": {"x": {At: 5, Mod: m}},
	}
	v := l.Valid()
	if len(v) != 1 || len(v["skyrimse"]) != 2 {
		t.Fatalf("got %+v", v)
	}
	if v["skyrimse"]["ok"].Hash != m.Hash() {
		t.Error("the hash must be computed here, not taken from the other PC")
	}
	if g := v["skyrimse"]["gone"]; !g.Gone || g.Mod != nil {
		t.Errorf("gone entry %+v", g)
	}
}

func TestVortexDBLocalParts(t *testing.T) {
	fakeVortexDB(t, map[string]string{
		"settings###profiles###lastActiveProfile###stardewvalley":                   `"p1"`,
		"persistent###profiles###p1###gameId":                                       `"stardewvalley"`,
		"persistent###mods###stardewvalley###Tractor###state":                       `"installed"`,
		"persistent###mods###stardewvalley###Tractor###attributes###version":        `"4.24.4"`,
		"persistent###mods###stardewvalley###Tractor###attributes###lastSMAPIQuery": `1790537882416`,
		"persistent###deployment###needToDeploy###stardewvalley":                    `false`,
	})
	db, err := OpenVortexDB()
	if err != nil {
		t.Fatal(err)
	}
	gm, _ := db.Mods("stardewvalley")
	m := gm.Mods["Tractor"]
	if _, ok := m.Leaves["attributes###lastSMAPIQuery"]; ok {
		t.Error("when this PC last asked for updates changes every session and must not be shared")
	}
	m.Enabled = true
	if err := db.Apply("stardewvalley", "p1", []VortexChange{{ID: "Tractor", Mod: &m}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if v, _ := dbGet(t, "persistent###mods###stardewvalley###Tractor###attributes###lastSMAPIQuery"); v != "1790537882416" {
		t.Errorf("this PC's own lastSMAPIQuery was not kept: %q", v)
	}
	if v, _ := dbGet(t, "persistent###deployment###needToDeploy###stardewvalley"); v != "true" {
		t.Errorf("Vortex should be asked to deploy after a change, needToDeploy = %q", v)
	}
}
