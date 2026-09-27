package mods

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ApolloF/syncer/internal/paths"
)

func TestCheckModSyncable(t *testing.T) {
	fv := setupVortex(t, true)
	ok := []struct{ kind, path string }{
		{KindStaging, fv.staging},
		{KindProfiles, filepath.Join(fv.root, "skyrimse", "profiles")},
		{KindDeployed, fv.data},
		{KindDeployed, fv.game}, // mods deployed into the game's main folder
	}
	for _, c := range ok {
		if err := CheckModSyncable(c.kind, c.path); err != nil {
			t.Errorf("CheckModSyncable(%s, %q) = %v, want nil", c.kind, c.path, err)
		}
	}

	notMarked := filepath.Join(fv.base, "Other")
	write(t, filepath.Join(notMarked, "x.txt"), "x")
	defaultStaging := filepath.Join(fv.root, "skyrimse", "mods") // no marker in this setup
	write(t, filepath.Join(fv.root, "skyrimse", "other", stagingMarker), "x")
	holdsGame := filepath.Join(fv.base, "Games")
	write(t, filepath.Join(holdsGame, stagingMarker), "x")
	lib := filepath.Join(fv.base, "Lib")
	write(t, filepath.Join(lib, "steamapps", "common", "x.txt"), "x")

	bad := []struct{ kind, path string }{
		{KindStaging, notMarked},
		{KindStaging, defaultStaging},
		{KindStaging, filepath.Join(fv.root, "skyrimse", "other")}, // marked, but not <game>\mods
		{KindStaging, holdsGame},
		{KindStaging, fv.root},
		{KindStaging, filepath.Join(fv.root, "state.v2")},
		{KindProfiles, filepath.Join(fv.root, "skyrimse", "profiles", "p1")},
		{KindProfiles, fv.staging},
		{KindDeployed, notMarked},
		{KindDeployed, holdsGame},
		{KindDeployed, filepath.Join(lib, "steamapps")},
		{KindDeployed, filepath.Join(lib, "steamapps", "common")},
		{KindStaging, `C:\`},
		{KindStaging, os.Getenv("WINDIR")},
		{KindDeployed, filepath.Join(os.Getenv("WINDIR"), "System32")},
		{KindStaging, os.Getenv("ProgramFiles")},
		{KindStaging, paths.Root(paths.Home)},
		{KindStaging, paths.Root(paths.Roaming)},
		{KindStaging, filepath.Join(fv.base, "missing")},
		{"other", fv.staging},
		{KindStaging, "relative"},
	}
	for _, c := range bad {
		if err := CheckModSyncable(c.kind, c.path); err == nil {
			t.Errorf("CheckModSyncable(%s, %q) = nil, want an error", c.kind, c.path)
		}
	}
}

func TestCheckModSyncableJunction(t *testing.T) {
	fv := setupVortex(t, true)
	link := filepath.Join(fv.base, "Link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, fv.staging).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction: %v %s", err, out)
	}
	if err := CheckModSyncable(KindStaging, link); err == nil {
		t.Error("a junction to a staging folder was accepted")
	}
}
