package mods

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckApplied(t *testing.T) {
	fv := setupDeployed(t)
	h, _ := hashFile(filepath.Join(fv.data, "SkyUI_SE.esp"))
	inv := Inventory{Files: []InvFile{{Rel: "SkyUI_SE.esp", Size: 6, SHA256: h}, {Rel: "meshes/sky.nif", Size: 4}}}
	if cs := CheckApplied(fv.data, inv, []string{"gone.esp"}); !Passed(cs) {
		t.Errorf("good folder failed: %+v", cs)
	}
	write(t, filepath.Join(fv.data, "SkyUI_SE.esp"), "tamper")
	cs := CheckApplied(fv.data, inv, []string{"meshes/vanilla.nif"})
	if Passed(cs) || len(Failed(cs)) != 2 {
		t.Errorf("wrong content and a leftover not caught: %+v", cs)
	}
	_ = os.Remove(filepath.Join(fv.data, "meshes", "sky.nif"))
	if Failed(CheckApplied(fv.data, inv, nil))[0][:len("Every mod file arrived")] != "Every mod file arrived" {
		t.Error("missing file not caught")
	}
}

func TestOutsideIgnoresBookkeeping(t *testing.T) {
	fv := setupDeployed(t)
	write(t, filepath.Join(fv.data, ".stignore"), "*")
	write(t, filepath.Join(fv.data, ".stfolder", "x"), "")
	write(t, filepath.Join(fv.data, "~syncthing~a.esp.tmp"), "")
	m, err := Outside(fv.data, map[string]bool{"skyui_se.esp": true})
	if err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "skyrim.esm", "meshes/sky.nif", "meshes/vanilla.nif":
		default:
			t.Errorf("unexpected %q", k)
		}
	}
	before := m
	write(t, filepath.Join(fv.data, "Skyrim.esm"), "patched!")
	after, _ := Outside(fv.data, map[string]bool{"skyui_se.esp": true})
	if CheckOutside(before, after).OK {
		t.Error("changed game file not caught")
	}
}

func TestCheckPlugins(t *testing.T) {
	fv := setupDeployed(t)
	if c, ok := CheckPlugins("skyrimse", fv.data); !ok || !c.OK {
		t.Errorf("CheckPlugins = %+v, %v", c, ok)
	}
	write(t, filepath.Join(fv.base, "Local", "Skyrim Special Edition", "plugins.txt"), "*SkyUI_SE.esp\n*Missing.esp\n")
	if c, _ := CheckPlugins("skyrimse", fv.data); c.OK || !c.Warn {
		t.Errorf("missing plugin: %+v", c)
	}
	if _, ok := CheckPlugins("cyberpunk2077", fv.data); ok {
		t.Error("plugins checked for a game without plugin lists")
	}
}

func TestAuditLog(t *testing.T) {
	setupDeployed(t)
	old := maxAudit
	maxAudit = 20
	defer func() { maxAudit = old }()
	for i := 0; i < maxAudit+5; i++ {
		LogAudit(AuditEntry{Folder: "a", Gen: int64(i), Checks: []Check{{Name: "x", OK: i%2 == 0}}})
	}
	LogAudit(AuditEntry{Folder: "b"})
	all := AuditLog("")
	if len(all) != maxAudit || all[0].Folder != "b" {
		t.Fatalf("log has %d entries, newest %+v", len(all), all[0])
	}
	a := AuditLog("a")
	if a[0].Gen != int64(maxAudit+4) || a[0].OK != (a[0].Gen%2 == 0) {
		t.Errorf("newest of a = %+v", a[0])
	}
}
