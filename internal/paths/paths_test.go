package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableRoundTrip(t *testing.T) {
	docs := Root(Documents)
	p := filepath.Join(docs, "My Games", "Foo")
	root, rel, ok := Portable(p)
	if !ok || root != Documents || rel != "My Games/Foo" {
		t.Fatalf("Portable = %q %q %v", root, rel, ok)
	}
	back, _ := Resolve(root, rel)
	if back != p {
		t.Fatalf("Resolve = %q", back)
	}
	low := filepath.Join(Root(LocalLow), "Studio", "Game")
	if root, _, _ := Portable(low); root != LocalLow {
		t.Fatalf("LocalLow classified as %q", root)
	}
	if _, _, ok := Portable(`Z:\elsewhere`); ok {
		t.Fatal("outside path should not be portable")
	}
	if !Within(`C:\A\B`, `c:\a\b\c`) || Within(`C:\A\B`, `C:\A\BC`) {
		t.Fatal("Within wrong")
	}
}

func TestResolveTraversal(t *testing.T) {
	cases := []struct {
		rel string
		ok  bool
	}{
		{"StardewValley/Saves", true},
		{`StardewValley\Saves`, true},
		{"", true},
		{"..", false},
		{"../..", false},
		{`..\secrets`, false},
		{"a/../../b", false},
		{"a/b/../../../c", false},
		{`C:\Windows`, false},
		{`C:x`, false},
		{`\Windows`, true}, // no volume, stays inside root once joined
	}
	for _, c := range cases {
		p, ok := Resolve(Documents, c.rel)
		if ok != c.ok {
			t.Errorf("Resolve(Documents, %q) ok = %v, want %v (p=%q)", c.rel, ok, c.ok, p)
			continue
		}
		if ok && !Within(Root(Documents), p) {
			t.Errorf("Resolve(Documents, %q) = %q escapes root", c.rel, p)
		}
	}
}

func TestValidID(t *testing.T) {
	valid := []string{"abcde-12345", "hollow-knight-2", "a", strings.Repeat("a", 64)}
	invalid := []string{
		"", "..", "a/b", `a\b`, "C:x", "con", "CON", "con.txt", "Nul.txt",
		"aux", "prn", "com1", "COM9", "lpt1", "trailing.",
		strings.Repeat("a", 65), "-leading-dash", ".leading-dot",
	}
	for _, id := range valid {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true, want false", id)
		}
	}
}

func TestCheckSyncable(t *testing.T) {
	ok := func(root, rel string) string {
		p, valid := Resolve(root, rel)
		if !valid {
			t.Fatalf("Resolve(%q, %q) failed", root, rel)
		}
		return p
	}
	allowed := []string{
		ok(Roaming, "StardewValley/Saves"),
		ok(Documents, "My Games/Skyrim"),
	}
	for _, p := range allowed {
		if err := CheckSyncable(p); err != nil {
			t.Errorf("CheckSyncable(%q) = %v, want nil", p, err)
		}
	}
	blocked := []string{
		ok(Home, ""),
		ok(Home, ".ssh"),
		ok(Home, "AppData"),
		ok(Local, "Syncthing"),
		ok(Roaming, "Microsoft/Windows/Start Menu/Programs/Startup"),
		ok(Documents, "My Games"),
		`C:\Windows`,
		`C:\`,
	}
	for _, p := range blocked {
		if err := CheckSyncable(p); err == nil {
			t.Errorf("CheckSyncable(%q) = nil, want error", p)
		}
	}
}
