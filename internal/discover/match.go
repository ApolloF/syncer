package discover

import (
	"path/filepath"
	"strings"
)

// SameGame reports whether a folder label names the game called name. An
// emulator save folder ("Game (RUNE saves)") counts as its game, and
// punctuation and case don't matter.
func SameGame(label, name string) bool {
	n := normalize(name)
	if len(n) < 3 {
		return false
	}
	return normalize(strings.TrimSpace(emuSuffix.ReplaceAllString(label, ""))) == n
}

// ManifestNames returns the names the game database uses for a Steam app
// or an install folder (either may be empty), from the cached index only.
func ManifestNames(steamID int, installDir string) []string {
	base := strings.ToLower(filepath.Base(filepath.Clean(installDir)))
	if installDir == "" {
		base = ""
	}
	var out []string
	for _, e := range CachedManifest() {
		match := steamID > 0 && e.SteamID == steamID
		for _, d := range e.InstallDirs {
			if base != "" && strings.ToLower(d) == base {
				match = true
			}
		}
		if match {
			out = append(out, e.Name)
		}
	}
	return out
}
