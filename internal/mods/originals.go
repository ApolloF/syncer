package mods

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ApolloF/syncer/internal/paths"
)

// Game files a deployment replaces on a receiving PC (a mod that overwrites
// one of the game's own loose files) are copied here first and kept for as
// long as the folder syncs: when the mod goes away, the game's own file
// comes back. Unlike snapshots they are never thinned out.

var originalsRoot = func() string { return filepath.Join(paths.Root(paths.Local), "Syncer", "mod-originals") }

func originalsIndex(folder string) string {
	return filepath.Join(originalsRoot(), folder, "index.json")
}

// Originals lists the game files kept for folder, by lower-cased path.
func Originals(folder string) map[string]string {
	m := map[string]string{}
	if !paths.ValidID(folder) {
		return m
	}
	if b, err := os.ReadFile(originalsIndex(folder)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// KeepOriginal copies the game's own file rel in target before a mod
// replaces it (once: the first copy is the game's).
func KeepOriginal(folder, target, rel string) error {
	if !paths.ValidID(folder) || !SafeUnder(target, rel) {
		return errors.New("bad path")
	}
	m := Originals(folder)
	k := strings.ToLower(rel)
	if _, ok := m[k]; ok {
		return nil
	}
	src := filepath.Join(target, filepath.FromSlash(rel))
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New(rel + " isn't a plain file")
	}
	if err := copyFile(src, filepath.Join(originalsRoot(), folder, "files", filepath.FromSlash(rel)), fi.ModTime()); err != nil {
		return err
	}
	m[k] = rel
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(originalsIndex(folder), b, 0o644)
}

// RestoreOriginal puts the game's own file rel back into target.
func RestoreOriginal(folder, target, rel string) error {
	if !paths.ValidID(folder) || !SafeUnder(target, rel) {
		return errors.New("bad path")
	}
	src := filepath.Join(originalsRoot(), folder, "files", filepath.FromSlash(rel))
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	return copyFile(src, filepath.Join(target, filepath.FromSlash(rel)), fi.ModTime())
}

// OriginalPath is where the kept copy of rel is.
func OriginalPath(folder, rel string) string {
	return filepath.Join(originalsRoot(), folder, "files", filepath.FromSlash(rel))
}
