package conflict

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestParse(t *testing.T) {
	for in, want := range map[string][2]string{
		"save.sync-conflict-20260925-143012-ABCDEFG.dat": {"save.dat", "ABCDEFG"},
		"slot.sync-conflict-20260925-143012-ABC2EFG":     {"slot", "ABC2EFG"},
		"a.b.sync-conflict-20260101-000000-ZZZZZZZ.sav":  {"a.b.sav", "ZZZZZZZ"},
	} {
		orig, dev, ok := Parse(in)
		if !ok || orig != want[0] || dev != want[1] {
			t.Errorf("Parse(%q) = %q %q %v", in, orig, dev, ok)
		}
	}
	for _, in := range []string{"save.dat", "save.sync-conflict.dat", ".sync-conflict-20260925-143012-ABCDEFG.dat"} {
		if _, _, ok := Parse(in); ok {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
}

func TestFindResolve(t *testing.T) {
	d := t.TempDir()
	hist := t.TempDir()
	var kept []string
	keep := func(abs, rel string, move bool) error {
		kept = append(kept, rel)
		dst := filepath.Join(hist, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.Remove(dst)
		if !move {
			return CopyFile(abs, dst)
		}
		return os.Rename(abs, dst)
	}
	write(t, filepath.Join(d, "sub", "save.dat"), "active")
	write(t, filepath.Join(d, "sub", "save.sync-conflict-20260925-143012-ABCDEFG.dat"), "other")
	write(t, filepath.Join(d, "gone.sync-conflict-20260925-143012-ABCDEFG.sav"), "orphan")
	write(t, filepath.Join(d, ".stversions", "x.sync-conflict-20260925-143012-ABCDEFG.sav"), "old")

	cs := Find(d)
	if len(cs) != 2 {
		t.Fatalf("want 2 conflicts, got %+v", cs)
	}
	if cs[0].Rel != "gone.sav" || !cs[0].Missing || cs[1].Rel != filepath.Join("sub", "save.dat") || cs[1].Missing || cs[1].Device != "ABCDEFG" {
		t.Fatalf("bad conflicts: %+v", cs)
	}

	// Use the other copy: it becomes the real file, the active one goes to history.
	if err := Resolve(d, cs[1].Copy, true, keep); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d, "sub", "save.dat")) != "other" || read(t, filepath.Join(hist, "sub", "save.dat")) != "active" {
		t.Fatal("use copy: wrong files")
	}
	// Keep current: the copy goes to history under the real name.
	if err := Resolve(d, cs[0].Copy, false, keep); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(hist, "gone.sav")) != "orphan" {
		t.Fatal("keep current: copy not preserved")
	}
	if n := Count(d); n != 0 {
		t.Fatalf("want 0 conflicts left, got %d", n)
	}
	if err := Resolve(d, cs[0].Copy, false, keep); err == nil {
		t.Fatal("resolving twice should fail")
	}
	for _, bad := range []string{"../x.sync-conflict-20260925-143012-ABCDEFG.dat", "sub/save.dat"} {
		if err := Resolve(d, bad, true, keep); err == nil {
			t.Errorf("Resolve(%q) should fail", bad)
		}
	}

	// Syncthing-style fallback history.
	write(t, filepath.Join(d, "a.sync-conflict-20260925-143012-ABCDEFG.sav"), "c")
	if err := Resolve(d, "a.sync-conflict-20260925-143012-ABCDEFG.sav", false, StVersionsKeep(d)); err != nil {
		t.Fatal(err)
	}
	es, _ := os.ReadDir(filepath.Join(d, ".stversions"))
	if len(es) != 2 { // x.sync-conflict… + a~<time>.sav
		t.Fatalf("stversions: %v", es)
	}
}

// "Use this one" while the game holds the save open: the swap fails, and the
// current save must still be there (not moved into history, which Syncthing
// would pass on to the other PCs as a delete).
func TestResolveLockedKeepsOriginal(t *testing.T) {
	d := t.TempDir()
	orig := filepath.Join(d, "save.dat")
	copyName := "save.sync-conflict-20260925-143012-ABCDEFG.dat"
	write(t, orig, "active")
	write(t, filepath.Join(d, copyName), "other")
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(orig), windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			windows.CloseHandle(h)
		}
	}()
	hist := filepath.Join(t.TempDir(), "save.dat")
	var moved []bool
	keep := func(abs, rel string, move bool) error {
		moved = append(moved, move)
		if move {
			return os.Rename(abs, hist)
		}
		// The game only blocks writers and deleters; reading for a copy works
		// in real life. Here the lock blocks reads too, so fake the copy.
		return os.WriteFile(hist, []byte("active"), 0o644)
	}
	if err := Resolve(d, copyName, true, keep); err == nil {
		t.Fatal("swap over a locked file should fail")
	}
	windows.CloseHandle(h)
	locked = false
	if len(moved) != 1 || moved[0] {
		t.Errorf("current file should be copied, not moved: %v", moved)
	}
	if read(t, orig) != "active" || read(t, filepath.Join(d, copyName)) != "other" {
		t.Error("files changed although the swap failed")
	}
	if err := Resolve(d, copyName, true, keep); err != nil {
		t.Fatal(err)
	}
	if read(t, orig) != "other" || read(t, hist) != "active" {
		t.Error("use copy after unlocking: wrong files")
	}
}

func TestFromDevice(t *testing.T) {
	cs := []Conflict{
		{Rel: "slot1.sav", Copy: "slot1.sync-conflict-20260925-143012-AAAAAAA.sav", Device: "AAAAAAA"},
		{Rel: "index.dat", Copy: "index.sync-conflict-20260925-143012-AAAAAAA.dat", Device: "AAAAAAA"},
		{Rel: "slot2.sav", Copy: "slot2.sync-conflict-20260925-143012-AAAAAAA.sav", Device: "AAAAAAA"},
		{Rel: "SLOT2.sav", Copy: "SLOT2.sync-conflict-20260925-150000-BBBBBBB.sav", Device: "BBBBBBB"},
	}
	var got []string
	for _, c := range FromDevice(cs, "AAAAAAA") {
		got = append(got, c.Rel)
	}
	if want := "slot1.sav index.dat"; strings.Join(got, " ") != want {
		t.Errorf("FromDevice = %q, want %q (slot2 has versions from two PCs)", got, want)
	}
	if len(FromDevice(cs, "CCCCCCC")) != 0 {
		t.Error("copies from a PC that made none")
	}
}

func TestDropIdentical(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "a.sav"), "same")
	write(t, filepath.Join(d, "a.sync-conflict-20260925-143012-ABCDEFG.sav"), "same")
	write(t, filepath.Join(d, "b.sav"), "mine")
	write(t, filepath.Join(d, "b.sync-conflict-20260925-143012-ABCDEFG.sav"), "else")
	same := func(a, b string) (bool, error) {
		x, err := os.ReadFile(a)
		if err != nil {
			return false, err
		}
		y, err := os.ReadFile(b)
		return string(x) == string(y), err
	}
	if n := DropIdentical(d, same); n != 1 {
		t.Errorf("dropped %d, want 1", n)
	}
	if cs := Find(d); len(cs) != 1 || cs[0].Rel != "b.sav" {
		t.Errorf("left: %+v", cs)
	}
}
