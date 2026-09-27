package mods

import (
	"os"
	"path/filepath"
	"testing"
)

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			m[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return m
}

func TestSnapshotRollback(t *testing.T) {
	fv := setupDeployed(t)
	pluginFile := filepath.Join(fv.base, "Local", "Skyrim Special Edition", "plugins.txt")
	before := readTree(t, fv.data)
	beforePlugins, _ := os.ReadFile(pluginFile)
	outside, err := Outside(fv.data, map[string]bool{"skyui_se.esp": true, "meshes/sky.nif": true, "new/added.esp": true})
	if err != nil {
		t.Fatal(err)
	}

	prev := &Inventory{Gen: 4, Files: []InvFile{{Rel: "SkyUI_SE.esp"}, {Rel: "meshes/sky.nif"}}}
	sn, err := TakeSnapshot("sky-data", "skyrimse", fv.data, prev, []string{"SkyUI_SE.esp", "meshes/sky.nif", "not-here.esp"}, []string{"new/added.esp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Copied) != 2 || sn.Gen != 4 {
		t.Fatalf("snapshot = %+v", sn)
	}
	// The update: one file changed, one removed, one added, plugins rewritten.
	write(t, filepath.Join(fv.data, "SkyUI_SE.esp"), "new version")
	_ = os.Remove(filepath.Join(fv.data, "meshes", "sky.nif"))
	write(t, filepath.Join(fv.data, "new", "added.esp"), "added")
	write(t, pluginFile, "*Other.esp\n")

	got := Snapshots("sky-data")
	if len(got) != 1 || got[0].Stamp != sn.Stamp {
		t.Fatalf("Snapshots = %+v", got)
	}
	if inv := got[0].Inventory(); inv == nil || inv.Gen != 4 {
		t.Errorf("snapshot inventory = %+v", inv)
	}
	if err := Rollback(got[0], fv.data); err != nil {
		t.Fatal(err)
	}
	after := readTree(t, fv.data)
	delete(after, "new") // an emptied folder may stay
	if len(after) != len(before) {
		t.Errorf("files after rollback: %v, before: %v", after, before)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s = %q after rollback, want %q", k, after[k], v)
		}
	}
	if b, _ := os.ReadFile(pluginFile); string(b) != string(beforePlugins) {
		t.Errorf("plugins.txt = %q after rollback", b)
	}
	now, _ := Outside(fv.data, map[string]bool{"skyui_se.esp": true, "meshes/sky.nif": true, "new/added.esp": true})
	if c := CheckOutside(outside, now); !c.OK {
		t.Errorf("game files changed: %s", c.Detail)
	}
	if err := Rollback(got[0], filepath.Join(fv.base, "elsewhere")); err == nil {
		t.Error("rollback into another folder allowed")
	}
}

func TestRollbackStaysInside(t *testing.T) {
	fv := setupDeployed(t)
	outsideFile := filepath.Join(fv.base, "keep.txt")
	write(t, outsideFile, "keep")
	sn := Snapshot{Stamp: "x", Folder: "sky-data", Target: fv.data, Added: []string{"../keep.txt", `..\keep.txt`}}
	_ = Rollback(sn, fv.data)
	if _, err := os.Stat(outsideFile); err != nil {
		t.Error("rollback deleted a file outside the target")
	}
}

func TestPruneSnapshots(t *testing.T) {
	fv := setupDeployed(t)
	for i := 0; i < 4; i++ {
		if _, err := TakeSnapshot("sky-data", "cyberpunk2077", fv.data, nil, []string{"SkyUI_SE.esp"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	PruneSnapshots("sky-data", 2, 0)
	if n := len(Snapshots("sky-data")); n != 2 {
		t.Errorf("%d snapshots left, want 2", n)
	}
}
