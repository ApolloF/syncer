package paths

import (
	"path/filepath"
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
