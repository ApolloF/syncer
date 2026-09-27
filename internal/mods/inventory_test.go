package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// setupDeployed is setupVortex plus plugin lists under a test folder, with
// a second mod file and a loose file of the game itself.
func setupDeployed(t *testing.T) fakeVortex {
	t.Helper()
	fv := setupVortex(t, true)
	oldPlugins, oldSnap, oldAudit := pluginRoot, snapshotRoot, auditFile
	pluginRoot = func() string { return filepath.Join(fv.base, "Local") }
	snapshotRoot = func() string { return filepath.Join(fv.base, "snapshots") }
	auditFile = func() string { return filepath.Join(fv.base, "audit.json") }
	t.Cleanup(func() { pluginRoot, snapshotRoot, auditFile = oldPlugins, oldSnap, oldAudit })
	write(t, filepath.Join(fv.base, "Local", "Skyrim Special Edition", "plugins.txt"), "# comment\n*SkyUI_SE.esp\n")
	write(t, filepath.Join(fv.data, "meshes", "sky.nif"), "mesh")
	write(t, filepath.Join(fv.data, "meshes", "vanilla.nif"), "game's own")
	m := Manifest{Version: 1, DeploymentMethod: MethodHardlink, GameID: "skyrimse", StagingPath: fv.staging, TargetPath: fv.data,
		Files: []DeployedFile{{RelPath: "SkyUI_SE.esp"}, {RelPath: `meshes\sky.nif`}, {RelPath: "gone.esp"}, {RelPath: "bad[1].esp"}}}
	b, _ := json.Marshal(m)
	write(t, filepath.Join(fv.data, "vortex.deployment.json"), string(b))
	return fv
}

func TestBuildInventory(t *testing.T) {
	fv := setupDeployed(t)
	inv, err := BuildInventory("skyrimse", fv.data, fv.game, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range inv.Files {
		rels = append(rels, f.Rel)
		if f.SHA256 == "" {
			t.Errorf("%s not hashed", f.Rel)
		}
	}
	if strings.Join(rels, " ") != "meshes/sky.nif SkyUI_SE.esp" {
		t.Errorf("files = %v", rels)
	}
	if !slices.Equal(inv.Skipped, []string{"bad[1].esp"}) {
		t.Errorf("skipped = %v", inv.Skipped)
	}
	if inv.Version.Exe == "" || !strings.HasPrefix(inv.Version.Exe, "skyrimse.exe|") {
		t.Errorf("version = %+v", inv.Version)
	}
	if inv.PluginLists["plugins.txt"] != "# comment\n*SkyUI_SE.esp\n" {
		t.Errorf("plugin lists = %v", inv.PluginLists)
	}
	inv.Folder, inv.Gen = "sky", 3 // not part of the checksum
	if err := inv.Valid(); err != nil {
		t.Errorf("Valid = %v", err)
	}
	bad := inv
	bad.Files = append([]InvFile{{Rel: "../../evil.dll", Size: 1}}, inv.Files...)
	if bad.Valid() == nil {
		t.Error("inventory with a path outside the target accepted")
	}
	tampered := inv
	tampered.Files = append([]InvFile(nil), inv.Files...)
	tampered.Files[0].Size++
	if tampered.Valid() == nil {
		t.Error("inventory not matching its checksum accepted")
	}
	evilList := inv
	evilList.PluginLists = map[string]string{"plugins.txt": `..\..\x.esp`}
	if evilList.Valid() == nil {
		t.Error("bad plugin list accepted")
	}
}

func TestBuildInventoryRefusesSymlinks(t *testing.T) {
	fv := setupDeployed(t)
	b, _ := json.Marshal(Manifest{GameID: "skyrimse", DeploymentMethod: MethodSymlink, Files: []DeployedFile{{RelPath: "SkyUI_SE.esp"}}})
	write(t, filepath.Join(fv.data, "vortex.deployment.json"), string(b))
	if _, err := BuildInventory("skyrimse", fv.data, fv.game, nil); err == nil {
		t.Error("symlink deployment accepted")
	}
	if _, err := BuildInventory("fallout4", fv.data, fv.game, nil); err == nil {
		t.Error("another game's deployment accepted")
	}
}

func TestDiffLocal(t *testing.T) {
	fv := setupDeployed(t)
	next := Inventory{Files: []InvFile{
		{Rel: "SkyUI_SE.esp", Size: 6},                // same size, no hash: same
		{Rel: "meshes/sky.nif", Size: 4, SHA256: "x"}, // hash differs: changed
		{Rel: "new.esp", Size: 10},                    // added
	}}
	prev := &Inventory{Files: []InvFile{{Rel: "SkyUI_SE.esp"}, {Rel: "meshes/vanilla.nif"}, {Rel: "old.esp"}}}
	d := DiffLocal(fv.data, prev, next)
	if d.Same != 1 || !slices.Equal(d.Changed, []string{"meshes/sky.nif"}) || !slices.Equal(d.Added, []string{"new.esp"}) {
		t.Errorf("diff = %+v", d)
	}
	// old.esp isn't here, so there's nothing to remove.
	if !slices.Equal(d.Removed, []string{"meshes/vanilla.nif"}) || d.Bytes != 14 {
		t.Errorf("removed = %v, bytes = %d", d.Removed, d.Bytes)
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b        Version
		same, known bool
	}{
		{Version{SteamBuild: "1"}, Version{SteamBuild: "1"}, true, true},
		{Version{SteamBuild: "1", Exe: "x"}, Version{SteamBuild: "2", Exe: "x"}, false, true},
		{Version{Exe: "a|1|h"}, Version{SteamBuild: "2", Exe: "A|1|h"}, true, true},
		{Version{Exe: "a|1|h"}, Version{Exe: "a|2|h"}, false, true},
		{Version{}, Version{Exe: "x"}, false, false},
	}
	for _, c := range cases {
		if same, known := SameVersion(c.a, c.b); same != c.same || known != c.known {
			t.Errorf("SameVersion(%+v, %+v) = %v, %v", c.a, c.b, same, known)
		}
	}
}

func TestSteamBuild(t *testing.T) {
	base := testDir(t)
	game := filepath.Join(base, "steamapps", "common", "Skyrim Special Edition")
	write(t, filepath.Join(game, "SkyrimSE.exe"), "exe")
	write(t, filepath.Join(base, "steamapps", "appmanifest_489830.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"489830\"\n\t\"installdir\"\t\t\"Skyrim Special Edition\"\n\t\"buildid\"\t\t\"12345\"\n}\n")
	if v := GameVersion(game); v.SteamBuild != "12345" {
		t.Errorf("SteamBuild = %q", v.SteamBuild)
	}
}

func TestValidPluginList(t *testing.T) {
	good := "# This file is used by the game\r\n*Unofficial Skyrim Patch.esp\nSkyUI_SE.esp\n\n*Light.esl\n"
	if err := ValidPluginList(good); err != nil {
		t.Errorf("good list: %v", err)
	}
	for _, bad := range []string{`*..\..\evil.esp`, "C:/x.esp", "readme.txt", "*a.esp\nrun.exe", strings.Repeat("a.esp\n", 6000)} {
		if ValidPluginList(bad) == nil {
			t.Errorf("accepted %q", bad[:min(len(bad), 30)])
		}
	}
	if got := Plugins(good); !slices.Equal(got, []string{"Unofficial Skyrim Patch.esp", "SkyUI_SE.esp", "Light.esl"}) {
		t.Errorf("Plugins = %v", got)
	}
}

func TestValidInvRel(t *testing.T) {
	for _, ok := range []string{"a.esp", "meshes/x/y.nif", "SKSE/Plugins/x.dll", "Interface/Translations/x (1).txt"} {
		if !ValidInvRel(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/../../x", `C:\x`, "/x", "x*", "x?y", "a[1]", "{a,b}", "!x", "#x", "(?i)x",
		"vortex.deployment.json", "Sub/vortex.deployment.foo.json", ".stignore", ".stfolder/x", "__folder_managed_by_vortex", "a:b"} {
		if ValidInvRel(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFindGameDir(t *testing.T) {
	fv := setupVortex(t, true)
	if got := FindGameDir("skyrimse"); got != fv.game {
		t.Errorf("FindGameDir = %q, want %q", got, fv.game)
	}
	if got := FindGameDir("fallout4"); got != "" {
		t.Errorf("FindGameDir(fallout4) = %q", got)
	}
	// A receiving PC's Vortex doesn't deploy the game: it's still found.
	if err := os.Remove(filepath.Join(fv.data, "vortex.deployment.json")); err != nil {
		t.Fatal(err)
	}
	if p, err := Resolve("game:skyrimse", "Data"); err != nil || p != fv.data {
		t.Errorf("Resolve on a receiver = %q, %v", p, err)
	}
}
