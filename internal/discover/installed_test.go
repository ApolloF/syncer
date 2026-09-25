package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"Hollow Knight™ ® ©", "hollowknight"},
		{"  Baldur's Gate: 3 - Deluxe_Edition!", "baldursgate3deluxeedition"},
		{"Été 日本語", "t"},
		{"ABCxyz019", "abcxyz019"},
		{"™®©-", ""},
		{"", ""},
	} {
		if got := normalize(tt.input); got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func testInstalled() *Installed {
	return &Installed{names: make(map[string]bool), dirNames: make(map[string]bool), steamIDs: make(map[int]bool)}
}

func TestInstalledMatches(t *testing.T) {
	i := testInstalled()
	i.add("Hollow Knight™", `D:\Games\HK`)
	i.add("", `D:\Games\Other Directory`)
	i.add("AB", "")
	i.steamIDs[413150] = true
	entries := map[string]Entry{
		"stardew valley": {Name: "Stardew Valley", SteamID: 413150},
		"another game":   {Name: "Another Game", InstallDirs: []string{"missing", "Other Directory"}},
		"absent game":    {Name: "Absent Game", SteamID: 999, InstallDirs: []string{"absent"}},
		"ab":             {Name: "AB", SteamID: 413150},
	}
	for _, tt := range []struct {
		label string
		want  bool
	}{
		{"HOLLOW KNIGHT", true}, {"Hollow-Knight®", true},
		{"Other Directory", true}, {"STARDEW VALLEY", true},
		{"Another Game", true}, {"Absent Game", false},
		{"Stardew-Valley", false}, {"Hollow", false},
		{"AB", false}, {"HK", false}, {"©", false}, {"", false},
	} {
		if got := i.matches(tt.label, entries); got != tt.want {
			t.Errorf("matches(%q) = %v, want %v", tt.label, got, tt.want)
		}
	}
}

func TestInstalledHasCachedManifest(t *testing.T) {
	cacheMu.Lock()
	old := cached
	cached = []Entry{{Name: "Manifest Title", SteamID: 42}}
	cacheMu.Unlock()
	t.Cleanup(func() {
		cacheMu.Lock()
		cached = old
		cacheMu.Unlock()
	})
	i := testInstalled()
	i.add("Direct Title", "")
	i.steamIDs[42] = true
	if !i.Has("direct title") || i.entries != nil {
		t.Fatal("direct match should not need the manifest")
	}
	if !i.Has("MANIFEST TITLE") {
		t.Fatal("missing cached Steam ID match")
	}
	cacheMu.Lock()
	cached = []Entry{{Name: "Replacement", SteamID: 42}}
	cacheMu.Unlock()
	if !i.Has("Manifest Title") || i.Has("Replacement") {
		t.Fatal("manifest map was not built exactly once")
	}
	if (*Installed)(nil).Has("anything") {
		t.Fatal("nil inventory matched")
	}
}

func TestInstalledRunning(t *testing.T) {
	i := testInstalled()
	i.add("Game", `D:\Games\Game`)
	i.add("Duplicate", `d:\GAMES\GAME\`)
	i.add("Uninstall Name", "")
	i.add("Invalid", `relative\folder`)
	i.add("Drive", `C:\`)
	if got := i.GameDirs(); !reflect.DeepEqual(got, []string{`D:\Games\Game`}) {
		t.Fatalf("game dirs: %v", got)
	}
	copy := i.GameDirs()
	copy[0] = `C:\`
	for _, tt := range []struct {
		paths []string
		want  bool
	}{
		{[]string{`d:\games\GAME\bin\game.exe`}, true},
		{[]string{`D:/Games/Game/game.exe`}, true},
		{[]string{`C:\Windows\explorer.exe`, `D:\Games\Game\game.exe`}, true},
		{[]string{`D:\Games\Game Extra\game.exe`}, false},
		{[]string{`D:\Games\Game\..\Other\game.exe`}, false},
		{[]string{`Games\Game\game.exe`}, false},
		{[]string{`C:\Windows\explorer.exe`}, false},
		{nil, false},
	} {
		if got := i.Running(tt.paths); got != tt.want {
			t.Errorf("Running(%v) = %v, want %v", tt.paths, got, tt.want)
		}
	}
	if (*Installed)(nil).Running([]string{`C:\game.exe`}) || (*Installed)(nil).GameDirs() != nil {
		t.Fatal("nil inventory is not empty")
	}
}

func writeStoreFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSteamMetadata(t *testing.T) {
	root, extra := t.TempDir(), t.TempDir()
	escape := func(s string) string { return strings.ReplaceAll(s, `\`, `\\`) }
	writeStoreFile(t, filepath.Join(root, "steamapps", "libraryfolders.vdf"),
		`"libraryfolders" { "0" { "path" "`+escape(root)+`" } "1" { "path" "`+escape(extra)+`" } "2" { "path" "relative" } }`)
	if got := steamLibraries(root); !reflect.DeepEqual(got, []string{root, extra}) {
		t.Fatalf("libraries: %v", got)
	}
	writeStoreFile(t, filepath.Join(extra, "steamapps", "appmanifest_367520.acf"),
		`"AppState" { "appid" "367520" "name" "Hollow Knight" "installdir" "Hollow Knight" }`)
	writeStoreFile(t, filepath.Join(extra, "steamapps", "appmanifest_bad.acf"),
		`"appid" "invalid" "name" "Bad Path" "installdir" "..\\.."`)
	writeStoreFile(t, filepath.Join(extra, "steamapps", "appmanifest_empty.acf"), `"appid" "-1"`)
	i := testInstalled()
	i.loadSteamLibrary(extra)
	if !i.Has("Hollow Knight") || !i.steamIDs[367520] || len(i.steamIDs) != 1 {
		t.Fatalf("Steam inventory: %+v", i)
	}
	want := []string{filepath.Join(extra, "steamapps", "common", "Hollow Knight")}
	if !reflect.DeepEqual(i.GameDirs(), want) {
		t.Fatalf("Steam dirs: %v, want %v", i.GameDirs(), want)
	}
	if got := steamLibraries(extra); !reflect.DeepEqual(got, []string{extra}) {
		t.Fatalf("missing VDF should retain Steam root: %v", got)
	}
	if got := steamLibraries(""); len(got) != 0 {
		t.Fatalf("empty Steam path: %v", got)
	}
}

func TestEpicMetadata(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ProgramData", root)
	dir := filepath.Join(root, "Epic", "EpicGamesLauncher", "Data", "Manifests")
	data, err := json.Marshal(map[string]string{"DisplayName": "Epic Game", "InstallLocation": `D:\Epic\GameFolder`})
	if err != nil {
		t.Fatal(err)
	}
	writeStoreFile(t, filepath.Join(dir, "valid.item"), string(data))
	writeStoreFile(t, filepath.Join(dir, "broken.item"), `{`)
	writeStoreFile(t, filepath.Join(dir, "relative.item"), `{"InstallLocation":"relative"}`)
	i := testInstalled()
	i.loadEpic()
	if !i.Has("Epic Game") || !i.Has("GameFolder") || !reflect.DeepEqual(i.GameDirs(), []string{`D:\Epic\GameFolder`}) {
		t.Fatalf("Epic inventory: %+v", i)
	}
}
