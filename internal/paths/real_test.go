package paths

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeProfile points Home, Roaming and Local at a temporary profile, so
// links can be made in it without touching the real one.
func fakeProfile(t *testing.T) (home, roaming, local string) {
	t.Helper()
	return fakeProfileIn(t, t.TempDir())
}

// fakeProfileIn is fakeProfile with the profile made in base.
func fakeProfileIn(t *testing.T, base string) (home, roaming, local string) {
	t.Helper()
	home = filepath.Join(base, "home")
	roaming = filepath.Join(home, "AppData", "Roaming")
	local = filepath.Join(home, "AppData", "Local")
	for _, d := range []string{roaming, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(SetRootForTest(Home, home))
	t.Cleanup(SetRootForTest(Roaming, roaming))
	t.Cleanup(SetRootForTest(Local, local))
	return home, roaming, local
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// junction links link to target, skipping the test where that can't be
// done or the link can't be followed.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction: %v %s", err, out)
	}
	if _, err := os.Stat(link); err != nil {
		t.Skipf("can't follow a junction here: %v", err)
	}
}

func shortName(t *testing.T, p string) string {
	t.Helper()
	u, _ := windows.UTF16PtrFromString(p)
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("no short name: %v", err)
	}
	s := windows.UTF16ToString(buf[:n])
	if filepath.Base(s) == filepath.Base(p) {
		t.Skip("short names are off on this volume")
	}
	return filepath.Join(filepath.Dir(p), filepath.Base(s))
}

func TestResolveRefusesOtherSpellings(t *testing.T) {
	for _, rel := range []string{
		"SSH~1", "MICROS~1/Windows/Start Menu/Programs/Startup", "Games/SAVEGA~1",
		"Microsoft::$INDEX_ALLOCATION/Windows", "Game/save:stream", "a:b",
	} {
		if p, ok := Resolve(Home, rel); ok {
			t.Errorf("Resolve(home, %q) = %q, want refused", rel, p)
		}
	}
}

// A paired PC names a folder through a junction, a short name or a stream:
// it must be refused, though its spelling is harmless.
func TestCheckSyncableRefusesAliases(t *testing.T) {
	home, roaming, _ := fakeProfile(t)
	mkdir(t, filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"))
	mkdir(t, filepath.Join(home, ".ssh"))
	real := mkdir(t, filepath.Join(home, "RealGames", "Game"))

	// Ordinary folders, existing or not, still pass.
	for _, p := range []string{real, filepath.Join(real, "New", "Deeper"), filepath.Join(home, "NotYet", "Saves")} {
		if err := CheckSyncable(p); err != nil {
			t.Errorf("CheckSyncable(%q) = %v, want nil", p, err)
		}
	}

	junction(t, filepath.Join(home, "Application Data"), roaming)
	junction(t, filepath.Join(home, "Games"), filepath.Join(home, "RealGames"))
	refused := []string{
		filepath.Join(home, "Application Data", "Microsoft", "Windows", "Start Menu", "Programs", "Startup"),
		filepath.Join(home, "Application Data", "Microsoft"),
		filepath.Join(home, "Application Data"),
		filepath.Join(home, "Games", "Game"),            // a link to a harmless folder is still a link
		filepath.Join(home, "Games", "Game", "Missing"), // below a link, not there yet
		filepath.Join(roaming, "Microsoft::$INDEX_ALLOCATION", "Windows"),
		filepath.Join(home, "Game:stream"),
	}
	for _, p := range refused {
		if err := CheckSyncable(p); err == nil {
			t.Errorf("CheckSyncable(%q) = nil, want refused", p)
		}
	}
	// A link to a harmless folder says so; one into a sensitive folder or
	// onto a whole root is plainly refused.
	for _, p := range refused[:5] {
		if err := CheckSyncable(p); errors.Is(err, ErrLink) != (p == refused[3] || p == refused[4]) {
			t.Errorf("CheckSyncable(%q) = %v", p, err)
		}
	}
	if err := CheckSensitive(refused[0]); err == nil {
		t.Error("CheckSensitive let a junction into Startup through")
	}
	if err := CheckContainer(filepath.Join(home, "Application Data")); err == nil {
		t.Error("CheckContainer let a junction to a whole root through")
	}

	ssh := shortName(t, filepath.Join(home, ".ssh"))
	if err := CheckSyncable(ssh); err == nil {
		t.Errorf("CheckSyncable(%q) = nil: short name of .ssh accepted", ssh)
	}
	if err := CheckSensitive(ssh); err == nil {
		t.Errorf("CheckSensitive(%q) = nil: short name of .ssh accepted", ssh)
	}
}

// A profile whose roots are spelled with a short name (as TEMP is on CI
// runners, C:\Users\RUNNER~1) still has its whole roots and containers
// refused when reached through a junction.
func TestCheckContainerShortNamedRoots(t *testing.T) {
	long := mkdir(t, filepath.Join(t.TempDir(), "a long profile folder"))
	home, roaming, _ := fakeProfileIn(t, shortName(t, long))
	mkdir(t, filepath.Join(home, "Documents"))
	t.Cleanup(SetRootForTest(Documents, filepath.Join(home, "Documents")))

	junction(t, filepath.Join(home, "Application Data"), roaming)
	junction(t, filepath.Join(home, "Docs"), filepath.Join(home, "Documents"))
	for _, p := range []string{
		filepath.Join(home, "Application Data"),
		filepath.Join(home, "Docs"),
		filepath.Join(long, "home", "AppData"),
	} {
		if err := CheckContainer(p); err == nil {
			t.Errorf("CheckContainer(%q) = nil, want refused", p)
		}
	}
	game := mkdir(t, filepath.Join(long, "home", "AppData", "Roaming", "Game"))
	if err := CheckContainer(game); err != nil {
		t.Errorf("CheckContainer(%q) = %v, want nil", game, err)
	}
}

// The junctions Windows itself puts in every profile.
func TestCheckSyncableRefusesLegacyProfileJunctions(t *testing.T) {
	for _, rel := range []string{
		`Application Data\Microsoft\Windows\Start Menu\Programs\Startup`,
		`Start Menu\Programs\Startup`,
		`Local Settings\Syncthing`,
		`Local Settings\Application Data\Microsoft`,
		`My Documents`,
	} {
		p := filepath.Join(Root(Home), rel)
		if _, err := os.Lstat(p); err != nil {
			continue // not in this profile
		}
		if err := CheckSyncable(p); err == nil {
			t.Errorf("CheckSyncable(%q) = nil, want refused", p)
		}
	}
}

// A profile that itself lives behind a junction (moved to another drive)
// still has syncable folders.
func TestCheckSyncableRootBehindJunction(t *testing.T) {
	base := t.TempDir()
	realHome := mkdir(t, filepath.Join(base, "RealHome"))
	mkdir(t, filepath.Join(realHome, "AppData", "Roaming", "Game"))
	link := filepath.Join(base, "home")
	junction(t, link, realHome)
	defer SetRootForTest(Home, link)()
	defer SetRootForTest(Roaming, filepath.Join(link, "AppData", "Roaming"))()
	defer SetRootForTest(Local, filepath.Join(link, "AppData", "Local"))()
	if err := CheckSyncable(filepath.Join(link, "AppData", "Roaming", "Game")); err != nil {
		t.Errorf("folder in a profile behind a junction refused: %v", err)
	}
	if err := CheckSyncable(filepath.Join(link, "AppData", "Roaming", "Microsoft")); err == nil {
		t.Error("sensitive folder in a profile behind a junction accepted")
	}
}
