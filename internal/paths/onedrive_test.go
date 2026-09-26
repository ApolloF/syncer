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

func TestTwinCandidates(t *testing.T) {
	game := `C:\Users\u\Documents\The Witcher 3`
	got := twinCandidates(`C:\Users\u\Documents`, false, []string{`C:\Users\u\OneDrive`}, `C:\Users\u`, game)
	if len(got) != 1 || got[0] != `C:\Users\u\OneDrive\Documents\The Witcher 3` {
		t.Errorf("local Documents: %v", got)
	}
	// Documents moved into OneDrive: the copy left behind in the profile.
	moved := `C:\Users\u\OneDrive\Documenten\Game`
	got = twinCandidates(`C:\Users\u\OneDrive\Documenten`, true, []string{`C:\Users\u\OneDrive`}, `C:\Users\u`, moved)
	if len(got) != 1 || got[0] != `C:\Users\u\Documents\Game` {
		t.Errorf("redirected Documents: %v", got)
	}
	// A localized Documents name is tried too.
	got = twinCandidates(`D:\Docs\Dokumente`, false, []string{`C:\Users\u\OneDrive`}, `C:\Users\u`, `D:\Docs\Dokumente\G`)
	if len(got) != 2 || got[1] != `C:\Users\u\OneDrive\Dokumente\G` {
		t.Errorf("localized: %v", got)
	}
	if got := twinCandidates(`C:\Users\u\Documents`, false, []string{`C:\Users\u\OneDrive`}, `C:\Users\u`, `C:\Users\u\AppData\Roaming\G`); got != nil {
		t.Errorf("outside Documents: %v", got)
	}
	if got := twinCandidates(`C:\Users\u\Documents`, false, nil, `C:\Users\u`, `C:\Users\u\Documents`); got != nil {
		t.Errorf("Documents itself: %v", got)
	}
}
