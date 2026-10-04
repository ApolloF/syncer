package mods

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction: %v %s", err, out)
	}
}

func TestValidInvRelWindowsNames(t *testing.T) {
	for _, bad := range []string{"a.esp.", "a.esp ", "dir./x.esp", "dir /x.esp", "CON", "nul.dll", "Sub/AUX.txt", "com1.esp", "a:stream"} {
		if ValidInvRel(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"console.esp", "Nulled.dll", "a..b.esp", "x/.hidden"} {
		if !ValidInvRel(ok) {
			t.Errorf("%q refused", ok)
		}
	}
}

// A folder in the game folder that links elsewhere must never be written
// through: not by a snapshot, a rollback or a kept original.
func TestSafeUnderJunction(t *testing.T) {
	fv := setupDeployed(t)
	elsewhere := filepath.Join(fv.base, "Elsewhere")
	write(t, filepath.Join(elsewhere, "victim.txt"), "keep me")
	junction(t, filepath.Join(fv.data, "linked"), elsewhere)
	if SafeUnder(fv.data, "linked/victim.txt") {
		t.Error("path through a junction accepted")
	}
	if !SafeUnder(fv.data, "meshes/sky.nif") {
		t.Error("ordinary path refused")
	}
	sn := Snapshot{Stamp: "x", Folder: "sky-data", Target: fv.data, Added: []string{"linked/victim.txt"}}
	_ = Rollback(sn, fv.data)
	if read := readTree(t, elsewhere); read["victim.txt"] != "keep me" {
		t.Error("rollback deleted a file through a junction")
	}
	if err := KeepOriginal("sky-data", fv.data, "linked/victim.txt"); err == nil {
		t.Error("original kept through a junction")
	}
	d := DiffLocal(fv.data, nil, Inventory{Files: []InvFile{{Rel: "linked/victim.txt", Size: 7}}})
	if len(d.Unsafe) != 1 || len(d.Added)+len(d.Changed) != 0 {
		t.Errorf("diff through a junction = %+v", d)
	}
}

func TestCheckModSyncableJunctionInside(t *testing.T) {
	fv := setupDeployed(t)
	real := filepath.Join(fv.base, "RealData")
	write(t, filepath.Join(real, "x.esp"), "x")
	// Written on the real side: Go doesn't treat a junction as a folder to create into.
	write(t, filepath.Join(real, "Sub", "y.esp"), "y")
	junction(t, filepath.Join(fv.game, "Linked"), real)
	sub := filepath.Join(fv.game, "Linked", "Sub")
	if err := CheckModSyncable(KindDeployed, sub); err == nil {
		t.Error("deployed folder below a junction accepted")
	}
}

func TestValidRequiresHashes(t *testing.T) {
	inv := Inventory{Files: []InvFile{{Rel: "SKSE/Plugins/evil.dll", Size: 900 << 20}}}
	inv.Hash = inv.sum()
	if inv.Valid() == nil {
		t.Error("program file without a checksum accepted")
	}
	inv = Inventory{Files: []InvFile{{Rel: "textures.bsa", Size: 900 << 20}}}
	inv.Hash = inv.sum()
	if err := inv.Valid(); err != nil {
		t.Errorf("big archive without a checksum refused: %v", err)
	}
	dup := Inventory{Files: []InvFile{{Rel: "a.bsa", Size: 900 << 20}, {Rel: "A.bsa", Size: 900 << 20}}}
	dup.Hash = dup.sum()
	if dup.Valid() == nil {
		t.Error("the same file listed twice accepted")
	}
}

func TestHashCache(t *testing.T) {
	fv := setupDeployed(t)
	cache := HashCache{}
	inv, err := BuildInventory("skyrimse", fv.data, fv.game, cache)
	if err != nil || len(cache) != len(inv.Files) {
		t.Fatalf("cache %d entries for %d files, %v", len(cache), len(inv.Files), err)
	}
	c := cache["skyui_se.esp"]
	c.SHA256 = "cached"
	cache["skyui_se.esp"] = c
	inv, _ = BuildInventory("skyrimse", fv.data, fv.game, cache)
	for _, f := range inv.Files {
		if f.Rel == "SkyUI_SE.esp" && f.SHA256 != "cached" {
			t.Error("unchanged file hashed again")
		}
	}
	write(t, filepath.Join(fv.data, "SkyUI_SE.esp"), "changed!")
	inv, _ = BuildInventory("skyrimse", fv.data, fv.game, cache)
	for _, f := range inv.Files {
		if f.Rel == "SkyUI_SE.esp" && (f.SHA256 == "cached" || len(f.SHA256) != 64) {
			t.Error("changed file not hashed again")
		}
	}
}

func TestOriginals(t *testing.T) {
	fv := setupDeployed(t)
	old := originalsRoot
	originalsRoot = func() string { return filepath.Join(fv.base, "originals") }
	defer func() { originalsRoot = old }()
	p := filepath.Join(fv.data, "meshes", "vanilla.nif")
	if err := KeepOriginal("sky-data", fv.data, "meshes/vanilla.nif"); err != nil {
		t.Fatal(err)
	}
	write(t, p, "a mod's version")
	// Kept once: a second call must not replace the game's copy with the mod's.
	if err := KeepOriginal("sky-data", fv.data, "meshes/vanilla.nif"); err != nil {
		t.Fatal(err)
	}
	if Originals("sky-data")["meshes/vanilla.nif"] != "meshes/vanilla.nif" {
		t.Errorf("index = %v", Originals("sky-data"))
	}
	_ = os.Remove(p)
	if err := RestoreOriginal("sky-data", fv.data, "meshes/vanilla.nif"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "game's own" {
		t.Errorf("restored %q", b)
	}
}
