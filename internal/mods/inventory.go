package mods

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/steam"
)

// An Inventory is what a PC sending deployed mods (the source) publishes
// for the PCs receiving them: exactly which files belong to the deployment,
// so a receiver knows what an update will change before anything arrives,
// can check afterwards that it got exactly that, and never touches a file
// of the game itself.
type Inventory struct {
	Folder  string    `json:"folder"` // folder id
	Game    string    `json:"game"`
	Gen     int64     `json:"gen"`  // goes up with every change
	Busy    bool      `json:"busy"` // the source is changing it right now (Vortex or the game is open)
	Updated time.Time `json:"updated"`
	Files   []InvFile `json:"files"`
	Version Version   `json:"version"`
	// PluginLists are the game's plugin lists (plugins.txt, loadorder.txt)
	// by file name, for games that have them.
	PluginLists map[string]string `json:"pluginLists,omitempty"`
	// Skipped are deployed files that can't be synced (their names can't be
	// written as an ignore pattern).
	Skipped []string `json:"skipped,omitempty"`
	// Hash sums up the files and plugin lists; equal hashes, equal content.
	Hash string `json:"hash"`
}

// InvFile is one deployed file.
type InvFile struct {
	Rel  string `json:"rel"` // relative to the deployment target, forward slashes
	Size int64  `json:"size"`
	// SHA256 is set for plugins and small files, which are the ones that
	// matter most and are cheap to hash.
	SHA256 string `json:"sha256,omitempty"`
}

// Version identifies the game's version, so mods deployed for one version
// never land in another.
type Version struct {
	SteamBuild string `json:"steamBuild,omitempty"` // Steam's build id
	Exe        string `json:"exe,omitempty"`        // name, size and hash of the game's main executable
}

// MaxInventoryFiles caps a deployment: every file is one ignore line.
const MaxInventoryFiles = 100_000

const hashLimit = 64 << 20

// MustHash says whether a deployed file is hashed: every file up to 64 MB,
// and every plugin and program file (the ones that run) whatever its size.
func MustHash(rel string, size int64) bool {
	return size <= hashLimit || isPlugin(rel) || IsCode(rel)
}

// IsCode reports whether a file is a program or script Windows or a game
// may run.
func IsCode(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".exe", ".dll", ".asi", ".bat", ".cmd", ".com", ".scr", ".ps1", ".vbs", ".js", ".jse", ".wsf", ".msi", ".lnk", ".sys", ".cpl":
		return true
	}
	return false
}

// HashCache remembers files' hashes by size and modification time, so a
// deployment is only hashed again where it changed.
type HashCache map[string]CachedHash

// CachedHash is one file's hash as last computed.
type CachedHash struct {
	Size   int64  `json:"size"`
	Mod    int64  `json:"mod"`
	SHA256 string `json:"sha256"`
}

// BuildInventory lists what Vortex deployed into target (a folder inside
// gameDir) according to its manifests there. Every file is hashed except
// big archives and media (hashing tens of GB on every change would take
// too long); program files and plugins always are. cache may be nil.
func BuildInventory(game, target, gameDir string, cache HashCache) (Inventory, error) {
	inv := Inventory{Game: game, Updated: time.Now().UTC()}
	ms, err := manifestsIn(target)
	if err != nil {
		return inv, err
	}
	seen := map[string]bool{}
	for _, m := range ms {
		if m.GameID != game {
			return inv, fmt.Errorf("the deployment in %s is for %s, not %s", target, m.GameID, game)
		}
		if w := methodProblem(m.DeploymentMethod); w != "" {
			return inv, errors.New(w)
		}
		for _, f := range m.Files {
			rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(f.RelPath)))
			k := strings.ToLower(rel)
			if seen[k] {
				continue
			}
			seen[k] = true
			if !ValidInvRel(rel) {
				inv.Skipped = append(inv.Skipped, f.RelPath)
				continue
			}
			p := filepath.Join(target, filepath.FromSlash(rel))
			fi, err := os.Lstat(p)
			if err != nil || !fi.Mode().IsRegular() || !SafeUnder(target, rel) {
				continue // listed but not there (or not a plain file): nothing to send
			}
			e := InvFile{Rel: rel, Size: fi.Size()}
			if MustHash(rel, fi.Size()) {
				c, ok := cache[k]
				if ok && c.Size == fi.Size() && c.Mod == fi.ModTime().UnixNano() {
					e.SHA256 = c.SHA256
				} else if e.SHA256, err = hashFile(p); err != nil {
					return inv, err
				} else if cache != nil {
					cache[k] = CachedHash{fi.Size(), fi.ModTime().UnixNano(), e.SHA256}
				}
			}
			inv.Files = append(inv.Files, e)
		}
	}
	if len(inv.Files) > MaxInventoryFiles {
		return inv, fmt.Errorf("the deployment has %d files; at most %d can be synced", len(inv.Files), MaxInventoryFiles)
	}
	sort.Slice(inv.Files, func(i, j int) bool { return strings.ToLower(inv.Files[i].Rel) < strings.ToLower(inv.Files[j].Rel) })
	inv.Version = GameVersion(gameDir)
	inv.PluginLists = ReadPluginLists(game)
	inv.Hash = inv.sum()
	return inv, nil
}

// manifestsIn reads the deployment manifests in dir.
func manifestsIn(dir string) ([]Manifest, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, e := range es {
		if e.Type().IsRegular() && isManifestName(e.Name()) {
			m, err := ReadManifest(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Vortex's deployment manifest is missing: deploy the mods in Vortex first")
	}
	return out, nil
}

// ManifestStamp changes whenever a deployment manifest in dir changes, as a
// cheap way to tell that an inventory needs building again.
func ManifestStamp(dir string) string {
	es, _ := os.ReadDir(dir)
	var b strings.Builder
	for _, e := range es {
		if e.Type().IsRegular() && isManifestName(e.Name()) {
			if fi, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s|%d|%d;", e.Name(), fi.Size(), fi.ModTime().UnixNano())
			}
		}
	}
	return b.String()
}

func (inv Inventory) sum() string {
	h := sha256.New()
	for _, f := range inv.Files {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", strings.ToLower(f.Rel), f.Size, f.SHA256)
	}
	names := make([]string, 0, len(inv.PluginLists))
	for n := range inv.PluginLists {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\n", n, inv.PluginLists[n])
	}
	fmt.Fprintf(h, "%s\x00%s", inv.Version.SteamBuild, inv.Version.Exe)
	return hex.EncodeToString(h.Sum(nil))
}

// Valid checks an inventory that arrived from another PC before it is used.
func (inv Inventory) Valid() error {
	if len(inv.Files) > MaxInventoryFiles {
		return errors.New("too many files")
	}
	seen := map[string]bool{}
	for _, f := range inv.Files {
		k := strings.ToLower(f.Rel)
		if !ValidInvRel(f.Rel) || f.Size < 0 || seen[k] {
			return fmt.Errorf("bad file %q", f.Rel)
		}
		seen[k] = true
		if MustHash(f.Rel, f.Size) && len(f.SHA256) != 64 {
			return fmt.Errorf("%q has no checksum", f.Rel)
		}
	}
	for n, c := range inv.PluginLists {
		if !pluginListNames[n] {
			return fmt.Errorf("bad plugin list %q", n)
		}
		if err := ValidPluginList(c); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	if inv.sum() != inv.Hash {
		return errors.New("its contents don't match its checksum")
	}
	return nil
}

// ValidInvRel reports whether rel can be a deployed file: a relative path
// that stays inside the target and can be written as a Syncthing ignore
// pattern (which has no escape character on Windows).
func ValidInvRel(rel string) bool {
	if !validRel(rel) || rel == "." || strings.ContainsAny(rel, "*?[]{}!#\\\r\n") || strings.HasPrefix(rel, "(") {
		return false
	}
	low := strings.ToLower(rel)
	return !isManifestName(filepath.Base(low)) && low != ".stignore" && !strings.HasPrefix(low, ".stfolder") &&
		!strings.HasPrefix(low, ".stversions") && filepath.Base(low) != managedMarker
}

func isPlugin(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".esp", ".esm", ".esl":
		return true
	}
	return false
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile is the SHA-256 of a file, as an inventory has it.
func HashFile(p string) (string, error) { return hashFile(p) }

// ---- differences ------------------------------------------------------------

// Diff is what applying an inventory changes in a folder.
type Diff struct {
	Added   []string `json:"added"`   // not here yet
	Changed []string `json:"changed"` // here, but different
	Removed []string `json:"removed"` // were deployed, no longer are
	Same    int      `json:"same"`
	// Bytes to download; RemovedPlugins are plugins that go away.
	Bytes          int64    `json:"bytes"`
	RemovedPlugins []string `json:"removedPlugins,omitempty"`
	// Unsafe are files whose folder here is a link: never written through.
	Unsafe []string `json:"unsafe,omitempty"`
}

// DiffLocal compares target with inventory next; prev is the inventory
// applied here before (nil for none), whose files that next no longer has
// are removed.
func DiffLocal(target string, prev *Inventory, next Inventory) Diff {
	var d Diff
	want := map[string]bool{}
	for _, f := range next.Files {
		want[strings.ToLower(f.Rel)] = true
		if !SafeUnder(target, f.Rel) {
			d.Unsafe = append(d.Unsafe, f.Rel)
			continue
		}
		p := filepath.Join(target, filepath.FromSlash(f.Rel))
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
			d.Added = append(d.Added, f.Rel)
			d.Bytes += f.Size
		case !sameFile(p, fi, f):
			d.Changed = append(d.Changed, f.Rel)
			d.Bytes += f.Size
		default:
			d.Same++
		}
	}
	if prev != nil {
		for _, f := range prev.Files {
			if want[strings.ToLower(f.Rel)] || !SafeUnder(target, f.Rel) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(target, filepath.FromSlash(f.Rel))); err == nil {
				d.Removed = append(d.Removed, f.Rel)
				if isPlugin(f.Rel) {
					d.RemovedPlugins = append(d.RemovedPlugins, f.Rel)
				}
			}
		}
	}
	return d
}

// sameFile reports whether the file at p (with info fi) is f.
func sameFile(p string, fi os.FileInfo, f InvFile) bool {
	if !fi.Mode().IsRegular() || fi.Size() != f.Size {
		return false
	}
	if f.SHA256 == "" {
		return true
	}
	h, err := hashFile(p)
	return err == nil && h == f.SHA256
}

// ---- game version -------------------------------------------------------------

// GameVersion identifies the version of the game installed in gameDir.
func GameVersion(gameDir string) Version {
	return Version{SteamBuild: steamBuild(gameDir), Exe: mainExe(gameDir)}
}

// SameVersion says whether two installs are the same game version: by
// Steam build when both have one, else by the main executable.
func SameVersion(a, b Version) (same bool, known bool) {
	if a.SteamBuild != "" && b.SteamBuild != "" {
		return a.SteamBuild == b.SteamBuild, true
	}
	if a.Exe != "" && b.Exe != "" {
		return strings.EqualFold(a.Exe, b.Exe), true
	}
	return false, false
}

// steamBuild reads Steam's build id for the game installed in gameDir
// (<library>\steamapps\common\<dir>).
func steamBuild(gameDir string) string {
	common := filepath.Dir(gameDir)
	if !strings.EqualFold(filepath.Base(common), "common") || !strings.EqualFold(filepath.Base(filepath.Dir(common)), "steamapps") {
		return ""
	}
	acfs, _ := filepath.Glob(filepath.Join(filepath.Dir(common), "appmanifest_*.acf"))
	for _, acf := range acfs {
		f, err := os.Open(acf)
		if err != nil {
			continue
		}
		st := steam.ParseVDF(io.LimitReader(f, 1<<20)).Get("AppState")
		f.Close()
		if st != nil && strings.EqualFold(st.Value("installdir"), filepath.Base(gameDir)) {
			return st.Value("buildid")
		}
	}
	return ""
}

// mainExe fingerprints the biggest executable in the game's folder, which
// is the game itself (a script extender's loader beside it is small).
func mainExe(gameDir string) string {
	es, _ := os.ReadDir(gameDir)
	var best os.FileInfo
	for _, e := range es {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".exe") {
			continue
		}
		if fi, err := e.Info(); err == nil && (best == nil || fi.Size() > best.Size()) {
			best = fi
		}
	}
	if best == nil {
		return ""
	}
	h, err := hashFile(filepath.Join(gameDir, best.Name()))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%s", strings.ToLower(best.Name()), best.Size(), h)
}

// ---- plugin lists -----------------------------------------------------------------

// pluginDirs are where Bethesda games keep their plugin lists, under
// %LOCALAPPDATA%, by Vortex game id.
var pluginDirs = map[string]string{
	"skyrimse": "Skyrim Special Edition", "skyrim": "Skyrim", "skyrimvr": "Skyrim VR",
	"enderal": "enderal", "enderalspecialedition": "Enderal Special Edition",
	"fallout4": "Fallout4", "fallout4vr": "Fallout4VR", "fallout3": "Fallout3", "falloutnv": "FalloutNV",
	"oblivion": "Oblivion", "morrowind": "Morrowind", "starfield": "Starfield",
}

var pluginListNames = map[string]bool{"plugins.txt": true, "loadorder.txt": true}

const maxPluginList = 256 << 10

// PluginListPath is where game's plugin list name is on this PC ("" if
// the game has none).
func PluginListPath(game, name string) string {
	dir, ok := pluginDirs[game]
	if !ok || !pluginListNames[name] {
		return ""
	}
	return filepath.Join(pluginRoot(), dir, name)
}

// pluginRoot holds the games' plugin list folders (a variable for tests).
var pluginRoot = func() string { return paths.Root(paths.Local) }

// ReadPluginLists reads game's plugin lists on this PC.
func ReadPluginLists(game string) map[string]string {
	out := map[string]string{}
	for name := range pluginListNames {
		p := PluginListPath(game, name)
		if p == "" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Size() > maxPluginList {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && ValidPluginList(string(b)) == nil {
			out[name] = string(b)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidPluginList checks a plugin list: comments, and plugin file names
// (optionally starting with * for "enabled"), nothing else.
func ValidPluginList(content string) error {
	if len(content) > maxPluginList {
		return errors.New("too big")
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 5000 {
		return errors.New("too many lines")
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		name := strings.TrimPrefix(l, "*")
		if len(name) > 255 || strings.ContainsAny(name, `/\:*?"<>|`+"\x00") || name == ".." || !isPlugin(name) {
			return fmt.Errorf("unexpected line %q", l)
		}
	}
	return nil
}

// Plugins lists the plugin names in a plugin list.
func Plugins(content string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, strings.TrimPrefix(l, "*"))
		}
	}
	return out
}

// FindGameDir finds where game is installed on this PC, for a PC whose own
// Vortex doesn't manage it: by the install folder names that game usually has.
func FindGameDir(game string) string {
	names := installNames[game]
	if n, ok := gameNames[game]; ok {
		names = append(names, n)
	}
	for _, gd := range gameDirs() {
		base := normalizeName(filepath.Base(gd))
		for _, n := range names {
			if base == normalizeName(n) {
				return gd
			}
		}
	}
	return ""
}

// installNames are games' usual install folder names, where they differ
// from the game's name.
var installNames = map[string][]string{
	"falloutnv": {"Fallout New Vegas"}, "fallout3": {"Fallout 3", "Fallout 3 goty"},
	"baldursgate3": {"Baldurs Gate 3"}, "witcher3": {"The Witcher 3 Wild Hunt"},
	"mountandblade2bannerlord": {"Mount & Blade II Bannerlord"}, "oblivion": {"Oblivion"},
	"oblivionremastered": {"Oblivion Remastered"}, "skyrim": {"Skyrim"}, "enderal": {"Enderal"},
	"eldenring": {"ELDEN RING"}, "darksouls3": {"DARK SOULS III"},
}

func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
