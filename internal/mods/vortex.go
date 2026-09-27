package mods

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/paths"
)

// Vortex (Nexus Mods' mod manager) keeps, per game, a staging folder with
// every installed mod (by default %APPDATA%\Vortex\<game>\mods, often moved
// next to the game so it can hardlink) and profiles with each profile's
// plugin list and load order. It deploys mods into the game's folder and
// leaves a vortex.deployment*.json there that names the staging folder.
//
// Its own state (which mods are enabled, their metadata) is a LevelDB
// database that is never synced: mods arriving in the staging folder show up
// in Vortex on the other PC, to be enabled and deployed there.
type Vortex struct{}

// StagingMarker is the file Vortex puts in every staging folder.
const StagingMarker = "__vortex_staging_folder"

// DeploysHere reports whether this PC's own Vortex deploys mods into dir.
func DeploysHere(dir string) bool {
	es, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range es {
		if e.Type().IsRegular() && isManifestName(e.Name()) {
			return true
		}
	}
	return false
}

const (
	vortexRootPrefix = "vortex:"
	stagingMarker    = StagingMarker
	// managedMarker is the file Vortex puts in folders it deploys into.
	managedMarker = "__folder_managed_by_vortex"
)

// vortexRoot is Vortex's data folder; SYNCER_VORTEX_ROOT overrides it (tests).
var vortexRoot = func() string {
	if v := os.Getenv("SYNCER_VORTEX_ROOT"); v != "" {
		return filepath.Clean(v)
	}
	return filepath.Join(paths.Root(paths.Roaming), "Vortex")
}

// gameDirs are the install folders of the games on this PC;
// SYNCER_TEST_GAME_DIRS (separated by ;) adds more (manual tests).
var gameDirs = func() []string {
	out := discover.CachedInstalled(2 * time.Minute).GameDirs()
	for _, d := range strings.Split(os.Getenv("SYNCER_TEST_GAME_DIRS"), ";") {
		if d = strings.TrimSpace(d); filepath.IsAbs(d) {
			out = append(out, filepath.Clean(d))
		}
	}
	return out
}

var gameIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// validGameID reports whether id looks like a Vortex game id.
func validGameID(id string) bool {
	return gameIDPattern.MatchString(id) && !strings.Contains(id, "..") && !notGames[id]
}

// notGames are folders in Vortex's data folder that aren't games.
var notGames = map[string]bool{
	"downloads": true, "temp": true, "plugins": true, "state.v2": true, "themes": true,
	"games": true, "extensions": true, "logs": true, "metadb": true, "__vortex_staging_folder": true,
}

// gameNames are Vortex game ids of popular moddable games.
var gameNames = map[string]string{
	"skyrimse": "Skyrim Special Edition", "skyrim": "Skyrim", "skyrimvr": "Skyrim VR",
	"enderal": "Enderal", "enderalspecialedition": "Enderal Special Edition",
	"fallout4": "Fallout 4", "fallout4vr": "Fallout 4 VR", "fallout3": "Fallout 3",
	"falloutnv": "Fallout: New Vegas", "fallout76": "Fallout 76", "oblivion": "Oblivion",
	"oblivionremastered": "Oblivion Remastered", "morrowind": "Morrowind", "starfield": "Starfield",
	"cyberpunk2077": "Cyberpunk 2077", "baldursgate3": "Baldur's Gate 3", "stardewvalley": "Stardew Valley",
	"witcher3": "The Witcher 3", "mountandblade2bannerlord": "Mount & Blade II: Bannerlord",
	"eldenring": "Elden Ring", "darksouls3": "Dark Souls III", "monsterhunterworld": "Monster Hunter: World",
	"monsterhunterwilds": "Monster Hunter Wilds", "residentevil42005": "Resident Evil 4",
	"valheim": "Valheim", "subnautica": "Subnautica", "kingdomcomedeliverance": "Kingdom Come: Deliverance",
	"kingdomcomedeliverance2": "Kingdom Come: Deliverance II", "dragonage": "Dragon Age: Origins",
	"dragonageinquisition": "Dragon Age: Inquisition", "masseffectlegendaryedition": "Mass Effect Legendary Edition",
	"x4foundations": "X4: Foundations", "bladeandsorcery": "Blade & Sorcery", "7daystodie": "7 Days to Die",
	"hogwartslegacy": "Hogwarts Legacy", "palworld": "Palworld", "sims4": "The Sims 4",
}

func gameName(id, gameDir string) string {
	if n, ok := gameNames[id]; ok {
		return n
	}
	if gameDir != "" {
		return filepath.Base(gameDir)
	}
	return id
}

func (Vortex) Name() string { return "vortex" }

func (Vortex) Owns(root string) bool {
	id, ok := strings.CutPrefix(root, vortexRootPrefix)
	if !ok {
		id, ok = strings.CutPrefix(root, rootGame)
	}
	return ok && validGameID(id)
}

func (Vortex) Running(procs []string) bool {
	for _, p := range procs {
		if strings.EqualFold(filepath.Base(p), "Vortex.exe") {
			return true
		}
	}
	return false
}

func (Vortex) Ignores(kind string) []string {
	switch kind {
	case KindStaging:
		// The marker names this PC's Vortex install; manifests and backups
		// are Vortex's bookkeeping for this PC's deployment.
		return []string{"/" + stagingMarker, "(?d)*.vortex_backup", "vortex.deployment*", "(?d)desktop.ini", "(?d)Thumbs.db"}
	case KindProfiles:
		return []string{"(?d)*.bak", "(?d)desktop.ini"}
	case KindDeployed:
		return []string{"vortex.deployment*.json", "/" + managedMarker, "(?d)*.vortex_backup"}
	}
	return nil
}

// vortexScan is what one look at Vortex's folders found.
type vortexScan struct {
	root      string
	games     map[string]bool // game ids with a folder in Vortex's data folder
	manifests []gameManifest
}

func scanVortex() vortexScan {
	vs := vortexScan{root: vortexRoot(), games: map[string]bool{}}
	if es, err := os.ReadDir(vs.root); err == nil {
		for _, e := range es {
			if id := strings.ToLower(e.Name()); e.IsDir() && validGameID(id) {
				vs.games[id] = true
			}
		}
	}
	if len(vs.games) == 0 {
		return vs // Vortex isn't used on this PC
	}
	for _, m := range findManifests(gameDirs()) {
		if validGameID(m.GameID) {
			vs.manifests = append(vs.manifests, m)
		}
	}
	return vs
}

// staging is the staging folder Vortex uses for game on this PC: the one
// its latest deployment names, else the default one. Either must hold
// Vortex's marker, so a stale or foreign folder is never used.
func (vs vortexScan) staging(game string) (string, bool) {
	for _, m := range vs.manifests {
		if m.GameID == game && m.StagingPath != "" && filepath.IsAbs(m.StagingPath) {
			if p := filepath.Clean(m.StagingPath); hasMarker(p) {
				return p, true
			}
		}
	}
	def := filepath.Join(vs.root, game, "mods")
	if vs.games[game] && hasMarker(def) {
		return def, true
	}
	return "", false
}

func (vs vortexScan) profiles(game string) (string, bool) {
	p := filepath.Join(vs.root, game, "profiles")
	if !vs.games[game] {
		return "", false
	}
	fi, err := os.Stat(p)
	return p, err == nil && fi.IsDir()
}

// deployments are game's deployment targets on this PC, one per folder.
func (vs vortexScan) deployments(game string) []gameManifest {
	var out []gameManifest
	seen := map[string]bool{}
	for _, m := range vs.manifests {
		k := strings.ToLower(m.Dir)
		if m.GameID == game && !seen[k] && paths.Within(m.GameDir, m.Dir) {
			seen[k] = true
			out = append(out, m)
		}
	}
	return out
}

func hasMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, stagingMarker))
	return err == nil
}

func (Vortex) Detect() []Found {
	vs := scanVortex()
	var out []Found
	gameDirOf := map[string]string{}
	games := map[string]bool{}
	for g := range vs.games {
		games[g] = true
	}
	for _, m := range vs.manifests {
		games[m.GameID] = true
		gameDirOf[m.GameID] = m.GameDir
	}
	for g := range games {
		name := gameName(g, gameDirOf[g])
		if p, ok := vs.staging(g); ok && CheckModSyncable(KindStaging, p) == nil {
			out = append(out, Found{Manager: "vortex", Game: g, GameName: name, Kind: KindStaging,
				Root: vortexRootPrefix + g, Rel: relStaging, Path: p})
		}
		if p, ok := vs.profiles(g); ok && CheckModSyncable(KindProfiles, p) == nil {
			out = append(out, Found{Manager: "vortex", Game: g, GameName: name, Kind: KindProfiles,
				Root: vortexRootPrefix + g, Rel: relProfiles, Path: p})
		}
		for _, m := range vs.deployments(g) {
			rel, err := filepath.Rel(m.GameDir, m.Dir)
			if err != nil {
				continue
			}
			f := Found{Manager: "vortex", Game: g, GameName: name, Kind: KindDeployed,
				Root: rootGame + g, Rel: filepath.ToSlash(rel), Path: m.Dir, GameDir: m.GameDir}
			mc := m.Manifest
			f.Deploy = &mc
			if err := CheckModSyncable(KindDeployed, m.Dir); err != nil {
				f.Warn = append(f.Warn, err.Error())
			}
			if rel == "." {
				f.Warn = append(f.Warn, "mods go into the game's main folder")
			}
			if w := methodProblem(m.DeploymentMethod); w != "" {
				f.Warn = append(f.Warn, w)
			}
			out = append(out, f)
		}
	}
	return out
}

// methodProblem says why a deployment method can't be synced ("" = it can).
func methodProblem(method string) string {
	switch method {
	case MethodHardlink, MethodMove, "":
		return ""
	}
	if strings.HasPrefix(method, "symlink") {
		return "Vortex deploys with symlinks, which can't be synced: switch Vortex to hardlink deployment"
	}
	return "unknown Vortex deployment method " + method
}

func (Vortex) Resolve(root, rel string) (string, error) {
	var game string
	var deployed bool
	switch {
	case strings.HasPrefix(root, vortexRootPrefix):
		game = strings.TrimPrefix(root, vortexRootPrefix)
	case strings.HasPrefix(root, rootGame):
		game, deployed = strings.TrimPrefix(root, rootGame), true
	default:
		return "", ErrUnsafe
	}
	if !validGameID(game) {
		return "", ErrUnsafe
	}
	vs := scanVortex()
	if deployed {
		return resolveDeployed(vs, game, rel)
	}
	var p string
	var ok bool
	var kind string
	switch rel {
	case relStaging:
		p, ok = vs.staging(game)
		kind = KindStaging
	case relProfiles:
		p, ok = vs.profiles(game)
		kind = KindProfiles
	default:
		return "", ErrUnsafe
	}
	if !ok {
		return "", ErrGameMissing
	}
	if CheckModSyncable(kind, p) != nil {
		return "", ErrUnsafe
	}
	return p, nil
}

// GameDir is where game is installed on this PC ("" if it isn't found).
func GameDir(game string) string {
	if !validGameID(game) {
		return ""
	}
	for _, m := range scanVortex().manifests {
		if m.GameID == game {
			return m.GameDir
		}
	}
	return FindGameDir(game)
}

func resolveDeployed(vs vortexScan, game, rel string) (string, error) {
	if !validRel(rel) {
		return "", ErrUnsafe
	}
	var gd string
	for _, m := range vs.manifests {
		if m.GameID == game {
			gd = m.GameDir
			break
		}
	}
	if gd == "" {
		gd = FindGameDir(game) // a PC receiving deployed mods: its Vortex doesn't deploy them
	}
	if gd == "" || !filepath.IsAbs(gd) {
		return "", ErrGameMissing
	}
	p := filepath.Join(gd, filepath.FromSlash(rel))
	if !paths.Within(gd, p) {
		return "", ErrUnsafe
	}
	return p, nil
}

// validRel reports whether rel is a relative path that stays below its base.
func validRel(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, `\`) || strings.HasPrefix(rel, "/") {
		return false
	}
	if strings.ContainsAny(rel, ":*?\"<>|\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}
