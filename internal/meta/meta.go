// Package meta keeps every paired PC's folder list in a small shared Syncthing
// folder ("syncer-meta"). Each PC writes only its own <deviceID>.json, so there
// are never write conflicts; every PC reads all files and adds the folders it
// is missing at the right local path. Pairing a new PC is therefore a single
// device-ID exchange: all saves follow automatically.
package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

const FolderID = "syncer-meta"

// SharedFolder is a folder described portably.
type SharedFolder struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Root  string `json:"root"`
	Rel   string `json:"rel"`
	// CopyOf: the publishing PC found this to be a Steam emulator's copy of
	// the named game's own saves, which other PCs don't add on their own.
	CopyOf string `json:"copyOf,omitempty"`

	// Mod folders (Root is a mods root, resolved by each PC's own mod
	// manager): their kind and the mod manager's game id.
	Kind    string `json:"kind,omitempty"`
	ModGame string `json:"modGame,omitempty"`
	// SizeGB is the folder's size, rounded up, for the auto-add limit.
	SizeGB int `json:"sizeGB,omitempty"`
	// Source is the PC whose deployed mods are sent to the others, and
	// SourceSince when it became the source (the newest claim wins).
	Source      string `json:"source,omitempty"`
	SourceSince int64  `json:"sourceSince,omitempty"`
	// Account: one account's saves of a split game (see package accounts).
	// Root and Rel are then the game's save folder, wherever this PC keeps
	// the folder right now.
	Account string `json:"account,omitempty"`
}

// DeviceFile is what one PC publishes.
type DeviceFile struct {
	Device  string         `json:"device"`
	Name    string         `json:"name"`
	Updated time.Time      `json:"updated"`
	Folders []SharedFolder `json:"folders"`
	// Features this PC's Syncer understands (e.g. accounts.Feature).
	Features []string `json:"features,omitempty"`
	// Accounts: this PC's copy of the accounts and split games, and who
	// plays on it.
	Accounts *accounts.Shared `json:"accounts,omitempty"`
}

// Dir is the local path of the meta folder.
func Dir() string { return filepath.Join(paths.AppDir(), "meta") }

// Report summarises what a reconcile changed.
type Report struct {
	Added  []string `json:"added"`
	Shared int      `json:"shared"`
}

// Reconcile brings Syncthing's config in line with the shared metadata.
func Reconcile(ctx context.Context, c *syncthing.Client) (Report, error) {
	var rep Report
	settings := store.LoadSettings()
	if settings.SyncDisabled || settings.Paused() {
		return rep, nil // "Undo everything" was used, or paused: leave Syncthing alone
	}
	st, err := c.Status(ctx)
	if err != nil {
		return rep, err
	}
	me := st.MyID
	devices, err := c.Devices(ctx)
	if err != nil {
		return rep, err
	}
	var others []string
	for _, d := range devices {
		if d.DeviceID != me {
			others = append(others, d.DeviceID)
		}
	}

	// Take in the accounts and split games other PCs published, and carry
	// out the splits and merges this PC hasn't yet, before looking at the
	// folders (they change the folder list).
	combineAccounts(me)
	if ApplyAccounts != nil {
		if err := ApplyAccounts(ctx, c, me, others); err != nil {
			warnOnce("accounts:"+err.Error(), "accounts: %v", err)
		}
	}
	ast := accounts.Load()
	ast.Clean()

	folders, err := c.Folders(ctx)
	if err != nil {
		return rep, err
	}

	byID := map[string]syncthing.Folder{}
	for _, f := range folders {
		byID[f.ID] = f
	}
	if _, ok := byID[FolderID]; !ok {
		if err := os.MkdirAll(Dir(), 0o755); err != nil {
			return rep, err
		}
		if err := c.AddFolder(ctx, map[string]any{
			"id": FolderID, "label": "Syncer (shared settings)", "path": Dir(), "type": "sendreceive",
			"fsWatcherEnabled": true, "rescanIntervalS": 600, "ignorePerms": true,
			"devices": devList(me, others),
		}); err != nil {
			return rep, err
		}
		byID[FolderID] = syncthing.Folder{ID: FolderID, Path: Dir(), Devices: toFD(devList(me, others))}
	}

	// Publish our own folder list.
	mine := DeviceFile{Device: me, Name: hostname(), Updated: time.Now(), Features: []string{accounts.Feature},
		Folders: publishable(folders, settings, ast, me, func(id string) int64 {
			st, _ := c.FolderStatus(ctx, id)
			return st.GlobalBytes
		})}
	rememberModSizes(settings, mine.Folders)
	if len(ast.Accounts) > 0 || len(ast.Records) > 0 {
		pub := ast.Shared
		pub.Active = ""
		if settings.Accounts || len(ast.Splits()) > 0 {
			pub.Active = ast.ActiveID() // who plays here (only with accounts on)
		}
		mine.Accounts = &pub
	}
	if changed(me, mine) {
		if err := store.WriteJSON(filepath.Join(Dir(), me+".json"), mine); err != nil {
			logx.Printf("meta: write own file: %v", err)
		}
	}

	// Adopt folders other PCs have. The most specific paths go first: when
	// one folder holds another (an old whole-vendor folder around a game's own
	// save folder), the game's folder is added and the one around it skipped.
	installed := lazyInstalled()
	synced := syncedPaths(folders)
	cands := readOthers(me)
	sort.SliceStable(cands, func(i, j int) bool { return resolvedLen(cands[i]) > resolvedLen(cands[j]) })
	for _, sf := range cands {
		if _, ok := byID[sf.ID]; ok {
			continue
		}
		if _, _, ok := accounts.ParseFolderID(sf.ID); ok {
			continue // added below, per game
		}
		if _, ok := ast.Split(sf.ID); ok {
			continue // split into per-account folders: an older PC still publishes it
		}
		p, reason := Adoptable(sf, settings, installed, synced)
		if reason == SkipUnsafe {
			warnOnce(sf.ID, "meta: not adopting %q (%s/%s): unsafe id or path", sf.ID, sf.Root, sf.Rel)
		}
		if reason != "" {
			continue
		}
		spec := FolderSpec{ID: sf.ID, Label: sf.Label, Path: p}
		if sf.Kind != "" {
			if err := RegisterMod(sf.ID, mods.NameOf(sf.ModGame), sf); err != nil {
				logx.Printf("meta: add %s: %v", sf.ID, err)
				continue
			}
			spec = ModSpec(sf.ID, sf.Label, p, sf.Kind, false)
		}
		if err := AddFolderSpec(ctx, c, spec, me, others); err != nil {
			logx.Printf("meta: add %s: %v", sf.ID, err)
			continue
		}
		byID[sf.ID] = syncthing.Folder{ID: sf.ID, Label: sf.Label, Path: p, Devices: toFD(devList(me, others)),
			Versioning: syncthing.Versioning{Type: "staggered"}, MaxConflicts: -1}
		synced = append(synced, p)
		rep.Added = append(rep.Added, sf.Label)
		logx.Printf("meta: added %s (%s) from another PC", sf.Label, p)
	}
	for _, add := range accountFolders(ast, cands, byID, settings, installed, synced) {
		if err := AddFolder(ctx, c, add.id, add.label, add.path, me, others); err != nil {
			logx.Printf("meta: add %s: %v", add.id, err)
			continue
		}
		byID[add.id] = syncthing.Folder{ID: add.id, Label: add.label, Path: add.path, Devices: toFD(devList(me, others)),
			Versioning: syncthing.Versioning{Type: "staggered"}}
		synced = append(synced, add.path)
		rep.Added = append(rep.Added, add.label)
		logx.Printf("meta: added %s (%s) from another PC", add.id, add.path)
	}

	// Every folder is shared with every paired PC, has versioning on and
	// keeps every conflict copy, except a PC receiving deployed mods: it
	// takes them from its source only. Mod folders keep the conflict limit
	// they were added with.
	for _, f := range byID {
		mf, isMod := settings.Mods[f.ID]
		if isMod && mf.Kind == mods.KindDeployed && mf.Role != RoleSource {
			continue
		}
		patch := patchFor(f, me, others)
		if isMod {
			delete(patch, "maxConflicts")
		}
		if _, ok := patch["devices"]; ok {
			// This PC's saves are about to meet another PC's: keep a
			// restore point of them first, as a PC joining a folder does.
			if f.ID != FolderID && BeforeShare != nil {
				if err := BeforeShare(ctx, f.ID, f.Label, f.Path); err != nil {
					logx.Printf("meta: not sharing %s yet: %v", f.ID, err)
					delete(patch, "devices")
				} else {
					rep.Shared++
				}
			} else {
				rep.Shared++
			}
		}
		if len(patch) > 0 {
			if err := c.PatchFolder(ctx, f.ID, patch); err != nil {
				logx.Printf("meta: patch %s: %v", f.ID, err)
			}
		}
	}

	cacheFolders(byID)
	return rep, nil
}

// publishable describes this PC's folders for the other PCs. Save folders
// are published by their portable path (an account's saves of a split game
// at the game's save folder); mod folders by their mod root,
// which each PC resolves with its own mod manager. size returns a folder's
// size in bytes (asked for mod folders only).
func publishable(folders []syncthing.Folder, s store.Settings, ast accounts.State, me string, size func(id string) int64) []SharedFolder {
	var out []SharedFolder
	for _, f := range folders {
		if f.ID == FolderID {
			continue
		}
		if g, a, ok := accounts.ParseFolderID(f.ID); ok {
			// Published at the game's save folder, not at the vault.
			if r, ok := ast.Split(g); ok {
				// The root is marked so older Syncers (which don't know
				// accounts) can't resolve it and never add one person's
				// saves at the game's save folder.
				out = append(out, SharedFolder{ID: f.ID, Label: r.Label, Root: accountRoot + r.Root, Rel: r.Rel, Account: a})
			}
			continue
		}
		if mf, ok := s.Mods[f.ID]; ok {
			if _, valid := mods.KindOf(mf.Root, mf.Rel); !valid {
				continue
			}
			if mf.Kind == mods.KindDeployed && mf.Role != RoleSource {
				continue // only the source's deployment is sent to the other PCs
			}
			sf := SharedFolder{ID: f.ID, Label: f.Label, Root: mf.Root, Rel: mf.Rel, Kind: mf.Kind, ModGame: mf.Game}
			if b := size(f.ID); b > 0 {
				sf.SizeGB = int((b + 1<<30 - 1) >> 30)
			} else {
				sf.SizeGB = s.Mods[f.ID].SizeGB // paused (Vortex open): keep the last size known
			}
			if mf.Kind == mods.KindDeployed {
				sf.Source, sf.SourceSince = me, sourceSince(f.ID)
			}
			out = append(out, sf)
			continue
		}
		if root, rel, ok := paths.Portable(f.Path); ok {
			out = append(out, SharedFolder{ID: f.ID, Label: f.Label, Root: root, Rel: rel,
				CopyOf: classify(f.Label, f.Path).CopyOf})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// rememberModSizes keeps the sizes published for mod folders, to publish
// again while Syncthing can't tell (paused while Vortex is open).
func rememberModSizes(s store.Settings, fs []SharedFolder) {
	changed := false
	for _, sf := range fs {
		if mf, ok := s.Mods[sf.ID]; ok && sf.SizeGB > 0 && mf.SizeGB != sf.SizeGB {
			changed = true
		}
	}
	if !changed {
		return
	}
	_, _ = store.UpdateSettings(func(s *store.Settings) {
		for _, sf := range fs {
			if mf, ok := s.Mods[sf.ID]; ok && sf.SizeGB > 0 {
				mf.SizeGB = sf.SizeGB
				s.Mods[sf.ID] = mf
			}
		}
	})
}

// Roles of a PC for a deployed-mods folder.
const (
	RoleSource   = "source"   // its deployment is sent to the other PCs
	RoleReceiver = "receiver" // it takes the source's deployment
)

// sourceSince is when this PC became the source of a deployed-mods folder.
var sourceSince = func(id string) int64 { return store.LoadState().ModSync[id].Since }

// BeforeJoin, when set, runs before this PC starts syncing a folder that is
// shared with other PCs, so the saves already at path can be protected first.
// An error stops the folder from being added (it is retried later).
var BeforeJoin func(ctx context.Context, id, label, path string) error

// BeforeShare, when set, runs before a folder this PC already syncs is shared
// with a PC it isn't shared with yet, so the saves here can be protected
// first. An error stops the sharing (it is retried on the next reconcile).
var BeforeShare func(ctx context.Context, id, label, path string) error

// patchFor returns the changes folder f needs: shared with every paired PC,
// and for game folders versioning on and every conflict copy kept (Syncthing
// deletes all but the newest 10, and a conflict copy may be the only copy of
// a save). The metadata folder has no versioning: each PC writes only its
// own file there.
func patchFor(f syncthing.Folder, me string, others []string) map[string]any {
	patch := map[string]any{}
	have := map[string]bool{}
	for _, d := range f.Devices {
		have[d.DeviceID] = true
	}
	missing := false
	for _, o := range others {
		if !have[o] {
			missing = true
		}
	}
	if missing {
		all := map[string]bool{me: true}
		for _, d := range f.Devices {
			all[d.DeviceID] = true
		}
		for _, o := range others {
			all[o] = true
		}
		ids := []string{me}
		for id := range all {
			if id != me {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids[1:])
		patch["devices"] = devList(ids[0], ids[1:])
	}
	if f.ID != FolderID && f.Versioning.Type == "" {
		patch["versioning"] = syncthing.StaggeredVersioning()
	}
	if f.ID != FolderID && f.MaxConflicts != -1 {
		patch["maxConflicts"] = -1
	}
	return patch
}

// BeforeAdd, when set, prepares a folder's directory just before Syncthing
// starts on it (e.g. writes the game's exclusions into .stignore, so the
// first scan already skips them), whichever way it started syncing.
var BeforeAdd func(id, path string)

// Reasons a folder published by another PC isn't added here.
const (
	SkipUnsafe       = "unsafe"        // bad id, or a path that must never be shared
	SkipRemoved      = "removed"       // the user stopped syncing it on this PC
	SkipBackupOnly   = "backup-only"   // backed up here but deliberately not synced
	SkipNotInstalled = "not-installed" // "only installed games" is on and it isn't
	SkipOverlap      = "overlap"       // holds, or sits in, a folder already synced here
	SkipOneDrive     = "onedrive"      // OneDrive already syncs that folder on this PC
	SkipSteamCloud   = "steam-cloud"   // Steam Cloud keeps that folder on this PC
	SkipCopy         = "copy"          // a Steam emulator's copy of saves the game keeps itself
	SkipUbisoftCloud = "ubisoft-cloud" // Ubisoft Connect's own save folder, in Ubisoft's cloud
	SkipNoRoot       = "not-here"      // the folder it's in doesn't exist on this PC (no Ubisoft Connect)

	// Mod folders.
	SkipModsOff             = "mods-off"              // "Find installed mods" is off here
	SkipModsExperimentalOff = "mods-experimental-off" // deployed mods: the experimental option is off here
	SkipModGameMissing      = "mod-game-missing"      // the mod manager doesn't manage that game here
	SkipModsManual          = "mods-manual"           // mod folders are added by hand here
	SkipModVortexHere       = "mod-vortex-here"       // deployed mods: this PC's own Vortex deploys that game

	// Pending: nothing stops adding it, it just hasn't happened yet (syncing
	// is paused or off here, or the next reconcile hasn't run).
	Pending = "pending"
)

// inOneDrive and classify are variables so tests don't depend on this PC's
// OneDrive, Steam or game database.
var (
	inOneDrive = paths.InOneDrive
	classify   = discover.Classify
	resolveMod = mods.Resolve
	checkMod   = mods.CheckModSyncable
	// deploysHere reports whether this PC's own mod manager deploys into
	// the deployed-mods folder at path.
	deploysHere = mods.DeploysHere
)

// Adoptable resolves a folder published by another PC to a local path and
// says why it must not be added here ("" = add it). Peers' metadata is
// untrusted: a compromised PC must not make this one share arbitrary folders,
// so the id and path are validated before anything else. synced are the paths
// of the folders this PC already syncs: two synced folders must never overlap,
// or the same files sync (and back up) twice.
func Adoptable(sf SharedFolder, s store.Settings, installed func(label string) bool, synced []string) (string, string) {
	if !paths.ValidID(sf.ID) || sf.ID == FolderID {
		return "", SkipUnsafe
	}
	if sf.Kind != "" || mods.IsModRoot(sf.Root) {
		return adoptableMod(sf, s, synced)
	}
	if paths.Known(sf.Root) && !paths.Here(sf.Root) {
		return "", SkipNoRoot
	}
	p, ok := paths.Resolve(sf.Root, sf.Rel)
	if !ok || paths.CheckSyncable(p) != nil {
		return "", SkipUnsafe
	}
	if reason := commonSkips(sf, p, s, synced); reason != "" {
		return p, reason
	}
	switch {
	case inOneDrive(p):
		return p, SkipOneDrive
	case sf.Root == paths.Ubisoft:
		return p, SkipUbisoftCloud
	case sf.CopyOf != "":
		return p, SkipCopy
	case s.InstalledOnly && !installed(sf.Label):
		return p, SkipNotInstalled
	}
	// Two sync tools on the same saves make Steam ask which copy to keep, and
	// the wrong pick overwrites a save. Asked last: it reads the disk.
	switch c := classify(sf.Label, p); {
	case c.SteamCloud:
		return p, SkipSteamCloud
	case c.CopyOf != "":
		return p, SkipCopy
	}
	return p, ""
}

// adoptableMod is Adoptable for a mod folder. Its root is resolved by this
// PC's own mod manager: nothing the peer sent is used as a path.
func adoptableMod(sf SharedFolder, s store.Settings, synced []string) (string, string) {
	kind, ok := mods.KindOf(sf.Root, sf.Rel)
	if !ok || kind != sf.Kind || sf.ModGame != mods.GameOf(sf.Root) {
		return "", SkipUnsafe
	}
	if !s.FindMods {
		return "", SkipModsOff
	}
	if kind == mods.KindDeployed && !s.SyncDeployedMods {
		return "", SkipModsExperimentalOff
	}
	p, err := resolveMod(sf.Root, sf.Rel)
	switch {
	case errors.Is(err, mods.ErrGameMissing):
		return "", SkipModGameMissing
	case err != nil:
		return "", SkipUnsafe
	}
	if err := checkMod(kind, p); err != nil {
		// A receiving PC's game folder may not have the deployment target
		// yet (it's created when the mods are applied): its parent must pass.
		if kind != mods.KindDeployed || !errors.Is(err, fs.ErrNotExist) || checkMod(kind, filepath.Dir(p)) != nil {
			return "", SkipUnsafe
		}
	}
	if reason := commonSkips(sf, p, s, synced); reason != "" {
		return p, reason
	}
	switch {
	case kind == mods.KindDeployed && deploysHere(p):
		return p, SkipModVortexHere
	case kind == mods.KindDeployed, !s.AutoAddMods:
		return p, SkipModsManual // deployed mods are never added on their own
	case s.ModsMaxGB > 0 && sf.SizeGB > s.ModsMaxGB:
		return p, SkipModsManual
	}
	return p, ""
}

// commonSkips are the reasons any folder, save or mod, isn't added.
func commonSkips(sf SharedFolder, p string, s store.Settings, synced []string) string {
	for _, lf := range s.BackupOnly {
		if lf.SyncID == sf.ID || lf.ID == sf.ID || paths.Within(lf.Path, p) || paths.Within(p, lf.Path) {
			return SkipBackupOnly
		}
	}
	for _, sp := range synced {
		if paths.Within(sp, p) || paths.Within(p, sp) {
			return SkipOverlap
		}
	}
	if s.Ignored[sf.ID] {
		return SkipRemoved
	}
	return ""
}

// PeerFolderAt returns the folder another PC syncs at path (resolved on this
// PC; me is this PC's device id), so this PC joins it under the same id
// instead of creating a second folder for the same saves.
func PeerFolderAt(me, path string) (SharedFolder, bool) {
	combineAccounts(me) // a split must be known before joining any of its folders
	want := filepath.Clean(path)
	for _, sf := range readOthers(me) {
		if !paths.ValidID(sf.ID) {
			continue
		}
		plain := sf
		plain.Root = strings.TrimPrefix(sf.Root, accountRoot)
		if p := resolveAny(plain); p != "" && strings.EqualFold(filepath.Clean(p), want) {
			return splitPlace(sf), true
		}
	}
	return SharedFolder{}, false
}

// syncedPaths lists the paths of the game folders Syncthing has.
func syncedPaths(fs []syncthing.Folder) []string {
	var out []string
	for _, f := range fs {
		if f.ID != FolderID {
			out = append(out, f.Path)
		}
	}
	return out
}

// resolvedLen is the length of a published folder's local path (0 if it
// doesn't resolve), to order folders from most to least specific.
func resolvedLen(sf SharedFolder) int {
	if sf.Kind != "" || mods.IsModRoot(sf.Root) {
		return 0 // resolving asks the mod manager; mod folders go last
	}
	p, _ := paths.Resolve(sf.Root, sf.Rel)
	return len(p)
}

// resolveAny resolves a published save or mod folder on this PC ("" if it
// doesn't resolve).
func resolveAny(sf SharedFolder) string {
	if sf.Kind != "" || mods.IsModRoot(sf.Root) {
		if k, ok := mods.KindOf(sf.Root, sf.Rel); !ok || k != sf.Kind {
			return ""
		}
		p, err := resolveMod(sf.Root, sf.Rel)
		if err != nil {
			return ""
		}
		return p
	}
	p, _ := paths.Resolve(sf.Root, sf.Rel)
	return p
}

// lazyInstalled looks up installed games only when asked, from a snapshot
// shared with the rest of the app: reconcile runs every minute.
func lazyInstalled() func(string) bool {
	var once sync.Once
	var inst *discover.Installed
	return func(label string) bool {
		once.Do(func() { inst = discover.CachedInstalled(2 * time.Minute) })
		return inst.Has(label)
	}
}

var (
	warnedMu sync.Mutex
	warned   = map[string]bool{}
)

// warnOnce logs once per key per process; reconcile runs every minute.
func warnOnce(key, format string, args ...any) {
	warnedMu.Lock()
	defer warnedMu.Unlock()
	if !warned[key] {
		warned[key] = true
		logx.Printf(format, args...)
	}
}

// Avail is a folder another PC syncs that this PC doesn't.
type Avail struct {
	SharedFolder
	Path   string
	From   string
	Reason string
}

// Available lists folders published by other PCs that this PC neither syncs
// nor backs up, with the reason they were skipped. Unsafe ones are never offered.
func Available(ctx context.Context, c *syncthing.Client) ([]Avail, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	folders, err := c.Folders(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, f := range folders {
		have[f.ID] = true
	}
	s := store.LoadSettings()
	installed := lazyInstalled()
	synced := syncedPaths(folders)
	var out []Avail
	for _, df := range readOtherFiles(st.MyID) {
		for _, sf := range df.Folders {
			if have[sf.ID] {
				continue
			}
			if g, _, ok := accounts.ParseFolderID(sf.ID); ok || isSplit(sf.ID) {
				// A split game is offered once, as the active account's saves.
				if !ok {
					g = sf.ID
				}
				if haveGame(have, g) {
					continue
				}
				sf = splitPlace(sf)
				sf.Root = strings.TrimPrefix(sf.Root, accountRoot)
				if have[sf.ID] {
					continue
				}
				check := sf
				check.ID = g
				p, reason := Adoptable(check, s, installed, synced)
				if reason == SkipUnsafe || reason == SkipBackupOnly || reason == SkipOverlap {
					continue
				}
				if reason == "" {
					reason = Pending
				}
				have[sf.ID] = true
				out = append(out, Avail{SharedFolder: sf, Path: p, From: df.Name, Reason: reason})
				continue
			}
			p, reason := Adoptable(sf, s, installed, synced)
			if reason == SkipUnsafe || reason == SkipBackupOnly || reason == SkipOverlap {
				continue
			}
			if reason == "" {
				reason = Pending
			}
			have[sf.ID] = true
			out = append(out, Avail{SharedFolder: sf, Path: p, From: df.Name, Reason: reason})
		}
	}
	out = dropOuter(out)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out, nil
}

// dropOuter keeps one folder of each nested group: a folder that holds
// another offered folder is left out (syncing both would sync those files
// twice), and of two folders at the same path the first is kept.
func dropOuter(av []Avail) []Avail {
	var out []Avail
	for i, a := range av {
		outer := false
		for j, b := range av {
			if i == j || !paths.Within(a.Path, b.Path) {
				continue
			}
			if !paths.Within(b.Path, a.Path) || j < i { // b is inside a, or the same path offered earlier
				outer = true
				break
			}
		}
		if !outer {
			out = append(out, a)
		}
	}
	return out
}

// AddFolder creates a Syncthing folder shared with all devices, with versioning.
func AddFolder(ctx context.Context, c *syncthing.Client, id, label, path, me string, others []string) error {
	return AddFolderSpec(ctx, c, FolderSpec{ID: id, Label: label, Path: path}, me, others)
}

// FolderSpec describes a folder to add. Zero values are a save folder's.
type FolderSpec struct {
	ID, Label, Path string
	Type            string         // "" = sendreceive
	Paused          bool           // added paused
	MarkerName      string         // "" = .stfolder
	Versioning      map[string]any // nil = staggered, kept in the folder
	MaxConflicts    *int           // nil = keep every conflict copy
}

// AddFolderSpec creates a Syncthing folder shared with all devices.
func AddFolderSpec(ctx context.Context, c *syncthing.Client, spec FolderSpec, me string, others []string) error {
	if BeforeJoin != nil && len(others) > 0 {
		if err := BeforeJoin(ctx, spec.ID, spec.Label, spec.Path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(spec.Path, 0o755); err != nil {
		return err
	}
	if BeforeAdd != nil {
		BeforeAdd(spec.ID, spec.Path)
	}
	f := map[string]any{
		"id": spec.ID, "label": spec.Label, "path": spec.Path, "type": "sendreceive",
		"fsWatcherEnabled": true, "rescanIntervalS": 3600, "ignorePerms": true,
		"devices":    devList(me, others),
		"versioning": syncthing.StaggeredVersioning(),
		// Keep every conflict copy: one may be the only copy of a save.
		"maxConflicts": -1,
	}
	if spec.Type != "" {
		f["type"] = spec.Type
	}
	if spec.Paused {
		f["paused"] = true
	}
	if spec.Versioning != nil {
		f["versioning"] = spec.Versioning
	}
	if spec.MarkerName != "" {
		f["markerName"] = spec.MarkerName
	}
	if spec.MaxConflicts != nil {
		f["maxConflicts"] = *spec.MaxConflicts
	}
	return c.AddFolder(ctx, f)
}

// ModVersionsDir is where replaced files of a mod folder are kept: outside
// the folder (so the mod manager never takes them for a mod), on this PC only.
func ModVersionsDir(id string) string {
	return filepath.Join(paths.Root(paths.Local), "Syncer", "mod-versions", id)
}

// ModSpec is how a mod folder of kind is added. A staging folder uses
// Vortex's own marker as Syncthing's, so no .stfolder appears among the mods
// and the folder stops, rather than looking emptied, if its drive is
// missing. Replaced files are kept for a week. A PC receiving deployed mods
// only takes them from the source, and stays paused between audited updates.
// HoldNewMod, when set, says whether a new mod folder must start paused
// (its mod manager is open) and remembers to resume it.
var HoldNewMod func(id string) bool

func ModSpec(id, label, path, kind string, source bool) FolderSpec {
	v := syncthing.StaggeredVersioning()
	v["params"] = map[string]string{"maxAge": "604800", "cleanInterval": "3600"}
	v["fsPath"] = ModVersionsDir(id)
	spec := FolderSpec{ID: id, Label: label, Path: path, Versioning: v}
	switch kind {
	case mods.KindStaging, mods.KindProfiles:
		if kind == mods.KindStaging {
			spec.MarkerName = mods.StagingMarker
		}
		spec.Paused = HoldNewMod != nil && HoldNewMod(id)
		// Syncthing's usual limit: conflict copies sit among the mods,
		// and replaced files are kept in the versions folder anyway.
		spec.MaxConflicts = new(10)
	case mods.KindDeployed:
		if source {
			spec.Type = "sendonly"
		} else {
			// No conflict copies in the game's folder: the snapshot taken
			// before each update already has the file it replaces.
			spec.Type, spec.Paused, spec.MaxConflicts = "receiveonly", true, new(int)
		}
	}
	return spec
}

// RegisterMod records a mod folder in the settings before it is added, so it
// is described as one to other PCs and its .stignore gets the mod lines. It
// is left out of the backup (mod folders are big and can be downloaded
// again) unless it was registered before; its backup toggle still works.
func RegisterMod(id, gameName string, sf SharedFolder) error {
	_, err := store.UpdateSettings(func(s *store.Settings) {
		old, had := s.Mods[id]
		if !had {
			s.NoBackup[id] = true
		}
		mf := store.ModFolder{Kind: sf.Kind, Manager: "vortex", Game: sf.ModGame, GameName: gameName,
			Root: sf.Root, Rel: sf.Rel, Role: old.Role}
		if sf.Kind == mods.KindDeployed && mf.Role == "" {
			mf.Role = RoleReceiver
		}
		s.Mods[id] = mf
	})
	return err
}

// NewID derives a stable, readable folder id from a game name.
func NewID(label string, taken map[string]bool) string {
	base := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if base == "" {
		base = "game"
	}
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-")
	}
	id := base
	for i := 2; taken[id] || id == FolderID; i++ {
		id = base + "-" + itoa(i)
	}
	return id
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func devList(me string, others []string) []map[string]string {
	l := []map[string]string{{"deviceID": me}}
	for _, o := range others {
		if o != me {
			l = append(l, map[string]string{"deviceID": o})
		}
	}
	return l
}

func toFD(l []map[string]string) []syncthing.FolderDevice {
	var out []syncthing.FolderDevice
	for _, m := range l {
		out = append(out, syncthing.FolderDevice{DeviceID: m["deviceID"]})
	}
	return out
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func changed(me string, df DeviceFile) bool {
	b, err := os.ReadFile(filepath.Join(Dir(), me+".json"))
	if err != nil {
		return true
	}
	var old DeviceFile
	if json.Unmarshal(b, &old) != nil || old.Name != df.Name || len(old.Folders) != len(df.Folders) {
		return true
	}
	oa, _ := json.Marshal(old.Accounts)
	na, _ := json.Marshal(df.Accounts)
	if string(oa) != string(na) || strings.Join(old.Features, ",") != strings.Join(df.Features, ",") {
		return true
	}
	for i := range old.Folders {
		if old.Folders[i] != df.Folders[i] {
			return true
		}
	}
	return false
}

// maxDeviceFile caps a peer's metadata file; real ones are a few KB.
const maxDeviceFile = 1 << 20

// readDeviceFiles parses every PC's published file, skipping oversized or
// malformed ones: they arrive from other PCs and aren't trusted.
func readDeviceFiles() []DeviceFile {
	es, _ := os.ReadDir(Dir())
	var out []DeviceFile
	for _, e := range es {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".json") {
			continue
		}
		if fi, err := e.Info(); err != nil || fi.Size() > maxDeviceFile {
			continue
		}
		b, err := os.ReadFile(filepath.Join(Dir(), n))
		if err != nil {
			continue
		}
		var df DeviceFile
		if json.Unmarshal(b, &df) == nil && df.Device != "" && df.Device+".json" == n {
			out = append(out, df)
		}
	}
	return out
}

func readOtherFiles(me string) []DeviceFile {
	var out []DeviceFile
	for _, df := range readDeviceFiles() {
		if df.Device != me {
			out = append(out, df)
		}
	}
	return out
}

// readOthers unions the folder lists published by other PCs.
func readOthers(me string) []SharedFolder {
	seen := map[string]bool{}
	var out []SharedFolder
	for _, df := range readOtherFiles(me) {
		for _, f := range df.Folders {
			if !seen[f.ID] && f.ID != FolderID {
				seen[f.ID] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// Peers returns names published by other PCs (device id -> name).
func Peers() map[string]string {
	m := map[string]string{}
	for _, df := range readDeviceFiles() {
		m[df.Device] = df.Name
	}
	return m
}

// ---- folder cache for offline backups ----------------------------------------

// CachedFolder is a folder as last seen in Syncthing.
type CachedFolder struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

func cacheFile() string { return filepath.Join(paths.AppDir(), "folders-cache.json") }

func cacheFolders(byID map[string]syncthing.Folder) {
	var out []CachedFolder
	for id, f := range byID {
		if id != FolderID {
			out = append(out, CachedFolder{ID: id, Label: f.Label, Path: f.Path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	_ = store.WriteJSON(cacheFile(), out)
}

// CachedFolders returns the last known folder list (used when Syncthing is down).
func CachedFolders() ([]CachedFolder, error) {
	b, err := os.ReadFile(cacheFile())
	if err != nil {
		return nil, errors.New("no folder list cached yet")
	}
	var out []CachedFolder
	return out, json.Unmarshal(b, &out)
}

// ForgetCache removes the cached folder list.
func ForgetCache() error {
	if err := os.Remove(cacheFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- deployed-mods inventories -----------------------------------------------

// A PC sending deployed mods publishes each folder's inventory next to its
// folder list, as <device>.modinv (not .json: those are folder lists).

const maxInventoryFile = 64 << 20

func inventoryFile(device string) string { return filepath.Join(Dir(), device+".modinv") }

// WriteModInventories publishes this PC's inventories (by folder id); none
// removes the file.
func WriteModInventories(me string, invs map[string]mods.Inventory) error {
	if len(invs) == 0 {
		if err := os.Remove(inventoryFile(me)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return store.WriteJSON(inventoryFile(me), invs)
}

// ModInventories reads the inventories a PC published (by folder id). They
// come from another PC and are checked before they are returned.
func ModInventories(device string) map[string]mods.Inventory {
	if !paths.ValidID(device) {
		return nil
	}
	p := inventoryFile(device)
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > maxInventoryFile {
		return nil
	}
	invCache.Lock()
	defer invCache.Unlock()
	if c, ok := invCache.m[device]; ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.invs
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var m map[string]mods.Inventory
	if json.Unmarshal(b, &m) != nil || len(m) > maxInventories {
		m = nil
	}
	for id, inv := range m {
		if !paths.ValidID(id) || inv.Folder != id || inv.Valid() != nil {
			delete(m, id)
		}
	}
	if invCache.m == nil {
		invCache.m = map[string]cachedInvs{}
	}
	invCache.m[device] = cachedInvs{fi.Size(), fi.ModTime(), m}
	return m
}

// maxInventories caps how many deployed-mods folders one PC can send.
const maxInventories = 64

type cachedInvs struct {
	size int64
	mod  time.Time
	invs map[string]mods.Inventory
}

// invCache keeps parsed inventories until their file changes: checking one
// hashes its whole file list.
var invCache struct {
	sync.Mutex
	m map[string]cachedInvs
}

// WrittenBy reports whether device itself last wrote its files in the
// shared metadata (its folder list and inventories). Any paired PC can write
// any file there; Syncthing records which one did.
func WrittenBy(ctx context.Context, c *syncthing.Client, device string) error {
	for _, name := range []string{device + ".json", device + ".modinv"} {
		by, err := c.ModifiedBy(ctx, FolderID, name)
		if err != nil {
			return err
		}
		if len(device) < 7 || !strings.EqualFold(by, device[:7]) {
			return fmt.Errorf("%s was last written by another PC (%s)", name, by)
		}
	}
	return nil
}

// Source is the PC currently sending a deployed-mods folder: the one with
// the newest claim among all PCs' folder lists (this PC's own included).
type Source struct {
	Device string
	Name   string
	Since  int64
	Folder SharedFolder
}

// ModSource finds who sends the deployed-mods folder id (ok false: no one).
func ModSource(id string) (Source, bool) {
	var best Source
	found := false
	for _, df := range readDeviceFiles() {
		for _, sf := range df.Folders {
			if sf.ID != id || sf.Kind != mods.KindDeployed || sf.Source != df.Device ||
				sf.SourceSince > time.Now().Add(time.Hour).UnixNano() { // a claim from the future would win forever
				continue
			}
			if !found || sf.SourceSince > best.Since {
				best, found = Source{Device: df.Device, Name: df.Name, Since: sf.SourceSince, Folder: sf}, true
			}
		}
	}
	return best, found
}
