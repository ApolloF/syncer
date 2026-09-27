package mods

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/paths"
)

// CheckModSyncable rejects a folder that must never be synced as a mod
// folder of kind. It is the mod folders' counterpart of paths.CheckSyncable:
// mod folders can lie outside the user profile (a staging folder next to the
// game, a game's Data folder), so instead of being limited to the known
// roots they must be what the mod manager uses, and never a drive, a system
// folder, a folder holding many games, or anything sensitive.
func CheckModSyncable(kind, abs string) error {
	if !filepath.IsAbs(abs) {
		return errors.New("not a full path")
	}
	abs = filepath.Clean(abs)
	fi, err := os.Lstat(abs)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("folder not found: %s: %w", abs, fs.ErrNotExist)
	}
	if isReparse(abs) {
		return errors.New("a linked folder (symlink or junction) can't be synced")
	}
	if filepath.Dir(abs) == abs {
		return errors.New("a whole drive can't be synced")
	}
	if paths.CheckContainer(abs) != nil || paths.CheckSensitive(abs) != nil {
		return errors.New("this folder can't be synced")
	}
	for _, sys := range systemDirs() {
		if paths.Within(abs, sys.dir) || sys.inside && paths.Within(sys.dir, abs) {
			return errors.New("a system folder can't be synced")
		}
	}
	if isLibraryDir(abs) {
		return errors.New("a folder holding many games can't be synced")
	}
	root := vortexRoot()
	if paths.Within(abs, root) {
		return errors.New("Vortex's own folder can't be synced")
	}
	games := gameDirs()
	switch kind {
	case KindStaging:
		if !hasMarker(abs) {
			return errors.New("not a Vortex staging folder")
		}
		if paths.Within(root, abs) && !isVortexGameSub(root, abs, "mods") {
			return errors.New("not a Vortex staging folder")
		}
		for _, gd := range games {
			if paths.Within(abs, gd) {
				return errors.New("this folder holds a game's install folder")
			}
		}
	case KindProfiles:
		if !isVortexGameSub(root, abs, "profiles") {
			return errors.New("not a Vortex profiles folder")
		}
	case KindDeployed:
		in := false
		for _, gd := range games {
			if paths.Within(gd, abs) {
				in = true
			}
			if paths.Within(abs, gd) && !strings.EqualFold(filepath.Clean(gd), abs) {
				return errors.New("this folder holds another game's install folder")
			}
		}
		if !in {
			return errors.New("not inside a game's install folder")
		}
		for d := abs; ; d = filepath.Dir(d) {
			if isReparse(d) {
				return errors.New("a linked folder (symlink or junction) can't be synced")
			}
			if isGameDir(games, d) || filepath.Dir(d) == d {
				break
			}
		}
		if paths.Within(root, abs) {
			return errors.New("not a game folder")
		}
	default:
		return errors.New("unknown kind of mod folder")
	}
	return nil
}

func isGameDir(games []string, d string) bool {
	for _, gd := range games {
		if strings.EqualFold(filepath.Clean(gd), d) {
			return true
		}
	}
	return false
}

// isVortexGameSub reports whether abs is <root>\<game>\<sub> for a valid game id.
func isVortexGameSub(root, abs, sub string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2 && validGameID(strings.ToLower(parts[0])) && strings.EqualFold(parts[1], sub)
}

type sysDir struct {
	dir    string
	inside bool // also block everything inside it
}

// systemDirs are folders that must never be synced; with inside, neither
// may anything in them. Games do install into Program Files, so only the
// folder itself is blocked there.
func systemDirs() []sysDir {
	var out []sysDir
	if d, err := windows.GetWindowsDirectory(); err == nil && d != "" {
		out = append(out, sysDir{d, true})
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "CommonProgramFiles", "CommonProgramFiles(x86)"} {
		if d := os.Getenv(env); filepath.IsAbs(d) {
			out = append(out, sysDir{filepath.Clean(d), false})
		}
	}
	if d := os.Getenv("ProgramData"); filepath.IsAbs(d) {
		out = append(out, sysDir{filepath.Clean(d), false})
	}
	return out
}

// isLibraryDir reports whether abs is (or holds) a store's library folder:
// steamapps, steamapps\common, XboxGames, an Epic or GOG games folder.
func isLibraryDir(abs string) bool {
	base := strings.ToLower(filepath.Base(abs))
	parent := strings.ToLower(filepath.Base(filepath.Dir(abs)))
	switch {
	case base == "steamapps", base == "common" && parent == "steamapps", base == "xboxgames":
		return true
	}
	for _, sub := range []string{"steamapps", `steamapps\common`} {
		if fi, err := os.Stat(filepath.Join(abs, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
