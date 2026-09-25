// Package paths maps absolute Windows paths to portable (root, rel) pairs so the
// same save folder resolves correctly on every PC, whatever the user name or
// Documents redirection (e.g. OneDrive) is.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

// Resolve turns a portable pair back into an absolute path on this PC. It
// refuses a rel that could escape root (absolute, has a drive, or contains a
// ".." segment): peers send (root, rel) pairs, and a malicious one must not
// be able to point Syncer outside the root it claims.
func Resolve(root, rel string) (string, bool) {
	base, ok := roots[root]
	if !ok {
		return "", false
	}
	if rel == "" || rel == "." {
		return base, true
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", false
	}
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", false
		}
	}
	abs := filepath.Join(base, filepath.FromSlash(rel))
	if _, ok := within(base, abs); !ok {
		return "", false
	}
	return abs, true
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

var validIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// reservedDeviceNames are Windows device names that can't be used as a file
// or directory name, with or without an extension.
var reservedDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidID reports whether id is safe to use as a directory/file name, e.g.
// for a Syncthing folder id received from a peer: peers are free to pick any
// id, so it must be checked before it is ever joined onto a filesystem path.
func ValidID(id string) bool {
	if !validIDPattern.MatchString(id) || strings.HasSuffix(id, ".") {
		return false
	}
	name := id
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return !reservedDeviceNames[strings.ToUpper(name)]
}

// rootSub is a path expressed as a known root plus subpath segments, so it
// can be resolved fresh from the current roots rather than hardcoded.
type rootSub struct {
	root string
	sub  []string
}

func (p rootSub) abs() (string, bool) {
	base, ok := roots[p.root]
	if !ok {
		return "", false
	}
	if len(p.sub) == 0 {
		return base, true
	}
	return filepath.Join(append([]string{base}, p.sub...)...), true
}

// protectedContainers are folders that hold many games' saves (or redirect
// to other roots): the container itself can't be shared wholesale, but its
// subfolders can, so a single game's save folder underneath is fine.
var protectedContainers = []rootSub{
	{Home, []string{"AppData"}},
	{Home, []string{"OneDrive"}},
	{Documents, nil},
	{Documents, []string{"My Games"}},
	{SavedGames, nil},
	{ProgramData, nil},
	{Public, nil},
}

// sensitivePaths are fully blocked, subtree and all: Syncer/Syncthing's own
// config (leaks the API key), browser profiles, credential/SSH stores, and
// the Startup folder (lives under Roaming\Microsoft) could all be used to
// compromise this PC if a malicious peer could make Syncer share them.
var sensitivePaths = []rootSub{
	{Local, []string{"Syncthing"}},
	{Roaming, []string{"Syncer"}},
	{Local, []string{"Syncer"}},
	{Roaming, []string{"Microsoft"}},
	{Local, []string{"Microsoft"}},
	{Local, []string{"Packages"}},
	{Local, []string{"Programs"}},
	{Local, []string{"Temp"}},
	{Local, []string{"Google"}},
	{Local, []string{"BraveSoftware"}},
	{Roaming, []string{"Mozilla"}},
	{Local, []string{"Mozilla"}},
	{Roaming, []string{"Opera Software"}},
	{ProgramData, []string{"Microsoft"}},
	{Home, []string{".ssh"}},
	{Home, []string{".aws"}},
	{Home, []string{".azure"}},
	{Home, []string{".gnupg"}},
	{Home, []string{".kube"}},
	{Home, []string{".docker"}},
	{Home, []string{".config"}},
	{Home, []string{"Desktop"}},
	{Home, []string{"Downloads"}},
}

var errNotSyncable = errors.New("only folders inside your user profile, Documents, AppData or Saved Games can be synced between PCs")

// CheckSyncable rejects an absolute path that must never become a shared
// folder: outside all known roots, a whole root or protected container (or
// an ancestor of one), or anywhere in a sensitive directory. Paired PCs
// auto-adopt folders published by their peers, so this is what stops a
// compromised or malicious peer from making Syncer share the whole profile,
// ~/.ssh, or Syncthing's own config.
func CheckSyncable(abs string) error {
	abs = filepath.Clean(abs)
	if _, _, ok := Portable(abs); !ok {
		return errNotSyncable
	}
	for _, r := range roots {
		if Within(abs, r) { // abs == r or abs is an ancestor of r
			return errNotSyncable
		}
	}
	for _, c := range protectedContainers {
		if p, ok := c.abs(); ok && Within(abs, p) { // abs == container or an ancestor of it
			return errNotSyncable
		}
	}
	for _, s := range sensitivePaths {
		p, ok := s.abs()
		if ok && (Within(p, abs) || Within(abs, p)) { // abs inside/== sensitive, or an ancestor of it
			return errNotSyncable
		}
	}
	return nil
}
