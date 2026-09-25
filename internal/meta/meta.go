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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
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
}

// DeviceFile is what one PC publishes.
type DeviceFile struct {
	Device  string         `json:"device"`
	Name    string         `json:"name"`
	Updated time.Time      `json:"updated"`
	Folders []SharedFolder `json:"folders"`
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
	folders, err := c.Folders(ctx)
	if err != nil {
		return rep, err
	}
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
	mine := DeviceFile{Device: me, Name: hostname(), Updated: time.Now()}
	for _, f := range folders {
		if f.ID == FolderID {
			continue
		}
		if root, rel, ok := paths.Portable(f.Path); ok {
			mine.Folders = append(mine.Folders, SharedFolder{ID: f.ID, Label: f.Label, Root: root, Rel: rel})
		}
	}
	sort.Slice(mine.Folders, func(i, j int) bool { return mine.Folders[i].ID < mine.Folders[j].ID })
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
		p, reason := Adoptable(sf, settings, installed, synced)
		if reason == SkipUnsafe {
			warnOnce(sf.ID, "meta: not adopting %q (%s/%s): unsafe id or path", sf.ID, sf.Root, sf.Rel)
		}
		if reason != "" {
			continue
		}
		if err := AddFolder(ctx, c, sf.ID, sf.Label, p, me, others); err != nil {
			logx.Printf("meta: add %s: %v", sf.ID, err)
			continue
		}
		byID[sf.ID] = syncthing.Folder{ID: sf.ID, Label: sf.Label, Path: p, Devices: toFD(devList(me, others)),
			Versioning: syncthing.Versioning{Type: "staggered"}}
		synced = append(synced, p)
		rep.Added = append(rep.Added, sf.Label)
		logx.Printf("meta: added %s (%s) from another PC", sf.Label, p)
	}

	// Every folder is shared with every paired PC and has versioning on.
	for _, f := range byID {
		have := map[string]bool{}
		for _, d := range f.Devices {
			have[d.DeviceID] = true
		}
		patch := map[string]any{}
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
			var ids []string
			for id := range all {
				ids = append(ids, id)
			}
			patch["devices"] = devList(ids[0], ids[1:])
			rep.Shared++
		}
		if f.ID != FolderID && f.Versioning.Type == "" {
			patch["versioning"] = syncthing.StaggeredVersioning()
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

// BeforeJoin, when set, runs before this PC starts syncing a folder that is
// shared with other PCs, so the saves already at path can be protected first.
// An error stops the folder from being added (it is retried later).
var BeforeJoin func(ctx context.Context, id, label, path string) error

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
)

// inOneDrive is a variable so tests don't depend on this PC's OneDrive.
var inOneDrive = paths.InOneDrive

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
	p, ok := paths.Resolve(sf.Root, sf.Rel)
	if !ok || paths.CheckSyncable(p) != nil {
		return "", SkipUnsafe
	}
	for _, lf := range s.BackupOnly {
		if lf.SyncID == sf.ID || lf.ID == sf.ID || paths.Within(lf.Path, p) || paths.Within(p, lf.Path) {
			return p, SkipBackupOnly
		}
	}
	for _, sp := range synced {
		if paths.Within(sp, p) || paths.Within(p, sp) {
			return p, SkipOverlap
		}
	}
	switch {
	case s.Ignored[sf.ID]:
		return p, SkipRemoved
	case inOneDrive(p):
		return p, SkipOneDrive
	case s.InstalledOnly && !installed(sf.Label):
		return p, SkipNotInstalled
	}
	return p, ""
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
	p, _ := paths.Resolve(sf.Root, sf.Rel)
	return len(p)
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
			p, reason := Adoptable(sf, s, installed, synced)
			if reason == SkipUnsafe || reason == SkipBackupOnly || reason == SkipOverlap {
				continue
			}
			if reason == "" {
				reason = SkipRemoved // not adopted yet, e.g. syncing is off here
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
	if BeforeJoin != nil && len(others) > 0 {
		if err := BeforeJoin(ctx, id, label, path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if BeforeAdd != nil {
		BeforeAdd(id, path)
	}
	return c.AddFolder(ctx, map[string]any{
		"id": id, "label": label, "path": path, "type": "sendreceive",
		"fsWatcherEnabled": true, "rescanIntervalS": 3600, "ignorePerms": true,
		"devices":    devList(me, others),
		"versioning": syncthing.StaggeredVersioning(),
	})
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
		if json.Unmarshal(b, &df) == nil && df.Device != "" {
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
