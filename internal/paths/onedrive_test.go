package paths

import "testing"

func TestWithinAny(t *testing.T) {
	roots := []string{`C:\Users\F\OneDrive`, `C:\Users\F\OneDrive - Work`}
	for p, want := range map[string]bool{
		`C:\Users\F\OneDrive\Documents\My Games\X`: true,
		`c:\users\f\onedrive - work\Saves`:         true,
		`C:\Users\F\OneDrive`:                      true,
		`C:\Users\F\OneDriveX\Saves`:               false,
		`C:\Users\F\Documents`:                     false,
	} {
		if got := WithinAny(roots, p); got != want {
			t.Errorf("%s: got %v", p, got)
		}
	}
	if WithinAny(nil, `C:\X`) {
		t.Error("no roots")
	}
}
