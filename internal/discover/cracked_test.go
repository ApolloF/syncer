package discover

import (
	"path/filepath"
	"testing"
)

func TestAppIDIn(t *testing.T) {
	d := t.TempDir()
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"steam_appid.txt":  {"1086940\r\n", 1086940},
		"steam_emu.ini":    {"[Settings]\r\n; comment\r\nAppId=1086940\r\nUserName=me\r\n", 1086940},
		"OnlineFix.ini":    {"[Main]\nFakeAppId=480\nRealAppId=1245620\n", 1245620},
		"spacewar.txt":     {"480", 0},
		"ColdClientLoader": {"[SteamClient]\nAppID = 413150\n", 413150},
		"empty.ini":        {"[x]\n", 0},
	} {
		p := filepath.Join(d, name)
		writeStoreFile(t, p, tt.body)
		if got := appIDIn(p); got != tt.want {
			t.Errorf("%s: got %d, want %d", name, got, tt.want)
		}
	}
	if appIDIn(filepath.Join(d, "missing.ini")) != 0 {
		t.Error("missing file")
	}
}

// A portable cracked copy: no Steam manifest, no uninstall entry, but
// Windows remembers its exe and the emulator beside it names the app id.
func TestLoadCracked(t *testing.T) {
	d := t.TempDir()
	bg3 := filepath.Join(d, "Games", "Baldurs Gate 3")
	writeStoreFile(t, filepath.Join(bg3, "bin", "bg3_dx11.exe"), "x")
	writeStoreFile(t, filepath.Join(bg3, "bin", "steam_emu.ini"), "[Settings]\nAppId=1086940\n")

	// Unreal layout: the exe at the top, the emulator further down; found
	// through the install folder of its uninstall entry.
	ue := filepath.Join(d, "Games", "Clair Obscur")
	writeStoreFile(t, filepath.Join(ue, "Expedition33.exe"), "x")
	writeStoreFile(t, filepath.Join(ue, "Sandfall", "Binaries", "Win64", "steam_settings", "steam_appid.txt"), "1903340")

	// An installer next to a copied steam_appid.txt doesn't count.
	dl := filepath.Join(d, "Downloads", "Game [Repack]")
	writeStoreFile(t, filepath.Join(dl, "setup.exe"), "x")
	writeStoreFile(t, filepath.Join(dl, "steam_appid.txt"), "999")

	gone := filepath.Join(d, "Removed", "game.exe") // uninstalled since
	i := testInstalled()
	exes := []string{filepath.Join(bg3, "bin", "bg3_dx11.exe"), gone, filepath.Join(dl, "setup.exe")}
	i.loadCracked(exes, []string{ue, filepath.Join(d, "Games", "Missing")})
	for id, want := range map[int]bool{1086940: true, 1903340: true, 999: false} {
		if i.steamIDs[id] != want {
			t.Errorf("app %d installed = %v, want %v", id, i.steamIDs[id], want)
		}
	}
	if !i.Running([]string{filepath.Join(bg3, "bin", "bg3_dx11.exe")}) || !i.Running([]string{filepath.Join(ue, "Expedition33.exe")}) {
		t.Errorf("cracked games' folders don't count as games: %v", i.roots)
	}

	i.entries = map[string]Entry{"baldur's gate 3": {Name: "Baldur's Gate 3", SteamID: 1086940}}
	i.once.Do(func() {}) // keep the entries above
	if !i.Has("Baldur's Gate 3 (RUNE saves)") {
		t.Error("cracked Baldur's Gate 3 not installed")
	}
}
