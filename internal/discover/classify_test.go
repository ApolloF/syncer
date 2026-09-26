package discover

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ApolloF/syncer/internal/steam"
)

// Baldur's Gate 3 run on RUNE: the emulator's remote\ folder holds the same
// saves (lower-cased) as the game's own PlayerProfiles.
func TestMirrorOf(t *testing.T) {
	d := t.TempDir()
	own := filepath.Join(d, "PlayerProfiles", "Public", "Savegames", "Story")
	emu := filepath.Join(d, "RUNE", "1086940")
	for i := 0; i < 8; i++ {
		n := fmt.Sprintf("Tav-%d__QuickSave_%d", i, i)
		writeStoreFile(t, filepath.Join(own, n, n+".lsv"), "x")
		if i < 6 {
			l := fmt.Sprintf("tav-%d__quicksave_%d", i, i)
			writeStoreFile(t, filepath.Join(emu, "remote", "_save_public", "savegames", "story", l, l+".lsv"), "x")
		}
	}
	writeStoreFile(t, filepath.Join(emu, "achievements.ini"), "x")
	if !mirrorOf(emu, []string{filepath.Join(d, "Missing"), own}) {
		t.Error("RUNE copy of the game's own saves not recognised")
	}

	// A cracked game whose only saves are the emulator's: nothing in common.
	solo := filepath.Join(d, "RUNE", "1091500")
	for i := 0; i < 5; i++ {
		writeStoreFile(t, filepath.Join(solo, "remote", fmt.Sprintf("slot%d.sav", i)), "x")
	}
	settings := filepath.Join(d, "Game", "Settings")
	writeStoreFile(t, filepath.Join(settings, "settings.ini"), "x")
	writeStoreFile(t, filepath.Join(settings, "slot0.sav"), "x")
	if mirrorOf(solo, []string{settings}) {
		t.Error("one shared name isn't a copy")
	}
	if mirrorOf(solo, nil) {
		t.Error("no folder of its own: not a copy")
	}
}

// fakeSteamDir is a Steam folder with one account (2); app 10 is installed
// through Steam when steamApp is set.
func fakeSteamDir(t *testing.T, steamApp bool) string {
	d := t.TempDir()
	writeStoreFile(t, filepath.Join(d, "config", "loginusers.vdf"), `"users" { "76561197960265730" { "AccountName" "me" } }`)
	writeStoreFile(t, filepath.Join(d, "userdata", "2", "config", "localconfig.vdf"), `"UserLocalConfigStore" {}`)
	if steamApp {
		writeStoreFile(t, filepath.Join(d, "steamapps", "appmanifest_10.acf"), `"AppState" { "appid" "10" "name" "Game" "StateFlags" "4" "installdir" "Game" }`)
		writeStoreFile(t, filepath.Join(d, "steamapps", "common", "Game", "game.exe"), "x")
	}
	return d
}

func TestCloudVerdict(t *testing.T) {
	saves := filepath.Join(t.TempDir(), "Game", "Saves")
	writeStoreFile(t, filepath.Join(saves, "save.sav"), "x")
	writeStoreFile(t, filepath.Join(saves, "steam_autocloud.vdf"), `"steam_autocloud.vdf" { "accountid" "2" }`)
	e := Entry{Name: "Game", SteamCloud: true, SteamID: 10}
	none := testInstalled()
	other := testInstalled()
	other.add("Game", `D:\Repacks\Game`) // installed, not through Steam

	elsewhere := steam.DetectIn(fakeSteamDir(t, false), steam.Host{ActiveUser: 2})
	viaSteam := steam.DetectIn(fakeSteamDir(t, true), steam.Host{ActiveUser: 2})
	for _, tt := range []struct {
		name    string
		e       Entry
		sc      *steam.Cloud
		emu     string
		inst    *Installed
		covered bool
		reason  string
	}{
		{"marker, not installed here", e, elsewhere, "", none, true, ""},
		{"marker, installed through another store", e, elsewhere, "", other, false, steam.ReasonNotInstalled},
		{"marker, cracked copy with emulator saves", e, elsewhere, "RUNE", none, false, "emulator:RUNE"},
		{"marker, Steam copy with old emulator saves", e, viaSteam, "RUNE", other, true, ""},
		{"no Steam Cloud support", Entry{Name: "Game", SteamID: 10}, viaSteam, "", none, false, ""},
	} {
		covered, reason := cloudVerdict(tt.e, saves, tt.sc, tt.emu, tt.inst)
		if covered != tt.covered || reason != tt.reason {
			t.Errorf("%s: got %v %q, want %v %q", tt.name, covered, reason, tt.covered, tt.reason)
		}
	}
}
