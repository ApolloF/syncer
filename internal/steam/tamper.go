package steam

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// crackMarkers are files that cracks and Steam emulators (Goldberg/GSE,
// CODEX, RUNE, SmartSteamEmu, OnlineFix, …) drop into a game folder. A game
// running on them talks to the emulator instead of Steam, so Steam Cloud
// never sees its saves.
var crackMarkers = map[string]bool{
	"steam_emu.ini":        true, // CODEX, RUNE and similar
	"coldclientloader.ini": true, // Goldberg/GSE experimental loader
	"steam_interfaces.txt": true, // Goldberg
	"local_save.txt":       true, // Goldberg portable saves
	"smartsteamemu.ini":    true,
	"onlinefix.ini":        true,
	"onlinefix64.dll":      true,
	"codex.ini":            true,
	"rune.ini":             true,
	"tenoke.ini":           true,
	"3dmgame.ini":          true,
	"ali213.ini":           true,
	"cpy.ini":              true,
	"steam_api.cdx":        true, // CODEX renames the real DLL
	"steam_api64.cdx":      true,
	"steam_api.rne":        true, // RUNE
	"steam_api64.rne":      true,
}

// unlockerMarkers belong to DLC unlockers (CreamAPI, SmokeAPI). They wrap
// the real steam_api DLL, so the game still runs through Steam and Steam
// Cloud keeps working; only the signature check is skipped for them.
var unlockerMarkers = map[string]bool{
	"cream_api.ini":        true,
	"smokeapi.json":        true,
	"smokeapi.config.json": true,
	"steam_api_o.dll":      true,
	"steam_api64_o.dll":    true,
}

const (
	// Folders below the install folder that are searched: Unreal games keep
	// steam_api64.dll in Engine\Binaries\ThirdParty\Steamworks\Steamv157\Win64.
	tamperDepth   = 7
	tamperEntries = 50000 // give up after this many entries (huge games)
)

// Tampered returns the first sign that a Steam game's folder was cracked or
// runs on a Steam emulator: a marker file, Goldberg's steam_settings folder,
// or a steam_api DLL without a valid (Valve) signature. "" if none.
func Tampered(dir string, signed func(string) bool) string {
	if dir == "" {
		return ""
	}
	var dlls []string
	unlocker := false
	found := ""
	n := 0
	root := filepath.Clean(dir)
	depth0 := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > tamperEntries {
			return filepath.SkipAll
		}
		name := strings.ToLower(d.Name())
		if d.IsDir() {
			if p != root && name == "steam_settings" {
				found = d.Name() // Goldberg/GSE configuration
				return filepath.SkipAll
			}
			if strings.Count(filepath.Clean(p), string(filepath.Separator))-depth0 >= tamperDepth {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case crackMarkers[name]:
			found = d.Name()
			return filepath.SkipAll
		case unlockerMarkers[name]:
			unlocker = true
		case name == "steam_api.dll" || name == "steam_api64.dll":
			dlls = append(dlls, p)
		}
		return nil
	})
	if found != "" || unlocker || signed == nil {
		return found
	}
	for _, p := range dlls {
		if !signed(p) {
			return filepath.Base(p) + " (unsigned or altered)"
		}
	}
	return ""
}
