package discover

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmulatorSaves(t *testing.T) {
	d := t.TempDir()
	rune := filepath.Join(d, "Steam", "RUNE")
	writeStoreFile(t, filepath.Join(rune, "1086940", "remote", "profile.lsf"), "save")
	writeStoreFile(t, filepath.Join(rune, "1086940", "achievements.ini"), "x")
	writeStoreFile(t, filepath.Join(rune, "1091500", "achievements.ini"), "x") // no saves
	writeStoreFile(t, filepath.Join(rune, "1091500", "remote", "sub", ".keep"), "")
	writeStoreFile(t, filepath.Join(rune, "notanid", "remote", "x.sav"), "x")
	gold := filepath.Join(d, "Goldberg SteamEmu Saves")
	writeStoreFile(t, filepath.Join(gold, "413150", "remote", "a", "b.sav"), "x")
	writeStoreFile(t, filepath.Join(gold, "settings", "account_name.txt"), "me")

	got := emulatorSaves([]emuDir{{rune, "RUNE"}, {gold, "Goldberg"}, {filepath.Join(d, "missing"), "X"}})
	want := []emuSave{
		{"Goldberg", 413150, filepath.Join(gold, "413150")},
		{"RUNE", 1086940, filepath.Join(rune, "1086940")},
		{"RUNE", 1091500, filepath.Join(rune, "1091500")}, // .keep counts: any file
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("emulatorSaves:\n got %v\nwant %v", got, want)
	}
}

func TestHasEmulatorLabel(t *testing.T) {
	i := testInstalled()
	i.add("Baldur's Gate 3", "")
	for label, want := range map[string]bool{
		"Baldur's Gate 3 (RUNE saves)":     true,
		"Baldur's Gate 3 (Goldberg saves)": true,
		"Baldur's Gate 3":                  true,
		"Baldur's Gate 3 (Deluxe)":         false,
	} {
		if got := i.Has(label); got != want {
			t.Errorf("Has(%q) = %v, want %v", label, got, want)
		}
	}
}
