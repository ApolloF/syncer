package conflict

import (
	"os"
	"path/filepath"
	"testing"
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
	keep := func(abs, rel string) error {
		kept = append(kept, rel)
		dst := filepath.Join(hist, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.Remove(dst)
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
