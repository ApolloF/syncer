package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ApolloF/syncer/internal/paths"
)

// testRoaming stands in for %APPDATA%: a folder of its own in the real
// one (Temp is protected, so t.TempDir can't be synced).
func testRoaming(t *testing.T) string {
	t.Helper()
	real := paths.Root(paths.Roaming)
	if real == "" {
		t.Skip("no AppData")
	}
	dir, err := os.MkdirTemp(real, "syncer-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Cleanup(paths.SetRootForTest(paths.Roaming, dir))
	return dir
}

func TestCheckLauncherData(t *testing.T) {
	roaming := testRoaming(t)
	ok := filepath.Join(roaming, "Seaglass", "Profile")
	other := filepath.Join(roaming, "discord", "Local Storage")
	for _, d := range []string{ok, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := checkLauncherData("Seaglass", ok); err != nil || p != ok {
		t.Errorf("good folder: %q, %v", p, err)
	}
	if p, err := checkLauncherData("seaglass", ok); err != nil || p != ok {
		t.Errorf("name in another case: %q, %v", p, err)
	}
	link := filepath.Join(roaming, "Seaglass", "Linked")
	linked := os.Symlink(other, link) == nil // needs developer mode or admin

	for _, c := range []struct{ launcher, path string }{
		{"", ok},                // no name
		{"discord", other},      // not a known launcher
		{"Seaglass", other},     // another program's folder
		{"Seaglass", "Profile"}, // relative
		{"Seaglass", filepath.Join(roaming, "Seaglass")}, // the launcher's whole folder
		{"Seaglass", roaming},                            // AppData itself
		{"Seaglass", filepath.Join(roaming, "Seaglass", "missing")},
		{"Seaglass", filepath.Join(roaming, "Seaglass.", "Profile")}, // Windows drops the dot
		{"Seaglass", filepath.Join(roaming, "Seaglass", "Profile ")},
		{"Seaglass", os.TempDir()}, // not in AppData
	} {
		if _, err := checkLauncherData(c.launcher, c.path); err == nil {
			t.Errorf("%q %q accepted", c.launcher, c.path)
		}
	}
	if linked {
		if _, err := checkLauncherData("Seaglass", link); err == nil {
			t.Errorf("a link to another folder accepted")
		}
	}
}

func TestTrailingDotPathsNotSyncable(t *testing.T) {
	roaming := testRoaming(t)
	for _, p := range []string{
		filepath.Join(roaming, "Microsoft.", "Windows", "Start Menu"),
		filepath.Join(roaming, "Game ", "Saves"),
	} {
		if paths.CheckSyncable(p) == nil {
			t.Errorf("%q syncable", p)
		}
	}
	if err := paths.CheckSyncable(filepath.Join(roaming, "Game", "Saves")); err != nil {
		t.Errorf("a plain folder: %v", err)
	}
}
