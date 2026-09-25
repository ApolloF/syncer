package winx

import (
	"testing"
	"unsafe"
)

// Recycle itself isn't tested: it would put files in this PC's Recycle Bin
// and show Windows' confirmation.

func TestSHFileOpStructLayout(t *testing.T) {
	// sizeof(SHFILEOPSTRUCTW) on 64-bit Windows.
	if n := unsafe.Sizeof(shFileOpStruct{}); n != 56 {
		t.Fatalf("shFileOpStruct is %d bytes, want 56", n)
	}
	if o := unsafe.Offsetof(shFileOpStruct{}.fAnyOperationsAborted); o != 36 {
		t.Fatalf("fAnyOperationsAborted at %d, want 36", o)
	}
}

func TestRecycleRejectsRelative(t *testing.T) {
	if err := Recycle("saves"); err == nil {
		t.Fatal("relative path accepted")
	}
}
