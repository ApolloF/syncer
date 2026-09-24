// Package paths maps absolute Windows paths to portable (root, rel) pairs so the
// same save folder resolves correctly on every PC, whatever the user name or
// Documents redirection (e.g. OneDrive) is.
package paths

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// Root identifiers. Stored in shared metadata, so never rename them.
const (
	Home        = "home"
	Documents   = "documents"
	SavedGames  = "savedgames"
	Roaming     = "roaming"
	Local       = "local"
	LocalLow    = "locallow"
	Public      = "public"
	ProgramData = "programdata"
)

var known = map[string]*windows.KNOWNFOLDERID{
	Home:        windows.FOLDERID_Profile,
	Documents:   windows.FOLDERID_Documents,
	SavedGames:  windows.FOLDERID_SavedGames,
	Roaming:     windows.FOLDERID_RoamingAppData,
	Local:       windows.FOLDERID_LocalAppData,
	LocalLow:    windows.FOLDERID_LocalAppDataLow,
	Public:      windows.FOLDERID_Public,
	ProgramData: windows.FOLDERID_ProgramData,
}

var roots map[string]string

func init() {
	roots = make(map[string]string, len(known))
	for name, id := range known {
		if p, err := windows.KnownFolderPath(id, 0); err == nil && p != "" {
			roots[name] = filepath.Clean(p)
		}
	}
	if _, ok := roots[Home]; !ok {
		if h, err := os.UserHomeDir(); err == nil {
			roots[Home] = h
		}
	}
}

// Root returns the absolute path of a root id ("" if unknown).
func Root(name string) string { return roots[name] }

// Roots returns a copy of all resolved roots.
func Roots() map[string]string {
	m := make(map[string]string, len(roots))
	for k, v := range roots {
		m[k] = v
	}
	return m
}

// Portable splits abs into the most specific known root and a relative path
// (forward slashes). ok is false when abs lies outside all known roots.
func Portable(abs string) (root, rel string, ok bool) {
	abs = filepath.Clean(abs)
	type cand struct{ name, path string }
	var cs []cand
	for n, p := range roots {
		cs = append(cs, cand{n, p})
	}
	sort.Slice(cs, func(i, j int) bool { return len(cs[i].path) > len(cs[j].path) })
	for _, c := range cs {
		if r, inside := within(c.path, abs); inside {
			return c.name, filepath.ToSlash(r), true
		}
	}
	return "", "", false
}

// Resolve turns a portable pair back into an absolute path on this PC.
func Resolve(root, rel string) (string, bool) {
	base, ok := roots[root]
	if !ok {
		return "", false
	}
	if rel == "" || rel == "." {
		return base, true
	}
	return filepath.Join(base, filepath.FromSlash(rel)), true
}

// Within reports whether child is parent or lies below it.
func Within(parent, child string) bool {
	_, ok := within(filepath.Clean(parent), filepath.Clean(child))
	return ok
}

func within(parent, child string) (string, bool) {
	if strings.EqualFold(parent, child) {
		return ".", true
	}
	p := strings.ToLower(parent)
	if !strings.HasSuffix(p, `\`) {
		p += `\`
	}
	if strings.HasPrefix(strings.ToLower(child), p) {
		return child[len(p):], true
	}
	return "", false
}

// AppDir is Syncer's own data directory (%APPDATA%\Syncer), created on demand.
func AppDir() string {
	d := filepath.Join(roots[Roaming], "Syncer")
	_ = os.MkdirAll(d, 0o755)
	return d
}
