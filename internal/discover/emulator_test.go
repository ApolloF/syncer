package discover

import (
	"os"
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

	uplay := filepath.Join(d, "Goldberg UplayEmu Saves")
	writeStoreFile(t, filepath.Join(uplay, "4740", "5a7f3c48.save"), "save")
	writeStoreFile(t, filepath.Join(uplay, "66088", "1.save"), "save")
	writeStoreFile(t, filepath.Join(uplay, "5093"), "not a folder")
	if err := os.MkdirAll(filepath.Join(uplay, "857"), 0o755); err != nil { // no saves
		t.Fatal(err)
	}

	got := emulatorSaves([]emuDir{{rune, "RUNE", false}, {gold, "Goldberg", false},
		{filepath.Join(d, "missing"), "X", false}, {uplay, "UplayEmu", true}})
	want := []emuSave{
		{"Goldberg", 413150, filepath.Join(gold, "413150"), false},
		{"UplayEmu", 4740, filepath.Join(uplay, "4740"), true}, // saves right in the game's folder
		{"UplayEmu", 66088, filepath.Join(uplay, "66088"), true},
		{"RUNE", 1086940, filepath.Join(rune, "1086940"), false},
		{"RUNE", 1091500, filepath.Join(rune, "1091500"), false}, // .keep counts: any file
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

func TestSavePathIn(t *testing.T) {
	d := t.TempDir()
	for content, want := range map[string]string{
		"[Settings]\nUserId=abc\nSavePath = \n":   "",
		"[Settings]\nSavePath=D:\\Saves\\Uplay\n": `D:\Saves\Uplay`,
		"SavePath = \"E:\\My Saves\"\r\n":         `E:\My Saves`,
		"savepath=saves\n":                        filepath.Join(d, "saves"),
		"[Settings]\nUserId=abc\n":                "",
	} {
		ini := filepath.Join(d, "upc_r2.ini")
		writeStoreFile(t, ini, content)
		if got := savePathIn(ini, d); got != want {
			t.Errorf("savePathIn(%q) = %q, want %q", content, got, want)
		}
	}
}

// A cracked Ubisoft game (VOICES38's Avatar: Frontiers of Pandora): the
// emulator's upc_r2.ini sits in the install folder, the exe below it.
func TestUbisoftCracked(t *testing.T) {
	d := t.TempDir()
	game := filepath.Join(d, "Avatar - Frontiers of Pandora")
	writeStoreFile(t, filepath.Join(game, "upc_r2.ini"), "SavePath =\n")
	writeStoreFile(t, filepath.Join(game, "bin", "afop.exe"), "x")
	if dir, save := ubisoftEmuNear(filepath.Join(game, "bin"), crackedLevels); dir != game || save != "" {
		t.Errorf("ubisoftEmuNear = %q, %q", dir, save)
	}
	if dir, _ := ubisoftEmuNear(filepath.Join(d, "Other"), crackedLevels); dir != "" {
		t.Errorf("found an ini where there's none: %q", dir)
	}
	i := testInstalled()
	i.loadUbisoftCracked([]string{filepath.Join(game, "bin", "afop.exe")})
	for _, label := range []string{"Avatar: Frontiers of Pandora", "Avatar: Frontiers of Pandora (UplayEmu saves)"} {
		if !i.Has(label) {
			t.Errorf("%q not seen as installed", label)
		}
	}
	if len(i.UbisoftSavePaths()) != 0 {
		t.Errorf("default save folder listed: %q", i.UbisoftSavePaths())
	}
}
