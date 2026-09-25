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
	"time"

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
	settings := store.LoadSettings()
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

	// Adopt folders other PCs have.
	for _, sf := range readOthers(me) {
		if _, ok := byID[sf.ID]; ok || settings.Ignored[sf.ID] {
			continue
		}
		p, ok := paths.Resolve(sf.Root, sf.Rel)
		if !ok {
			continue
		}
		if err := AddFolder(ctx, c, sf.ID, sf.Label, p, me, others); err != nil {
			logx.Printf("meta: add %s: %v", sf.ID, err)
			continue
		}
		byID[sf.ID] = syncthing.Folder{ID: sf.ID, Label: sf.Label, Path: p, Devices: toFD(devList(me, others)),
			Versioning: syncthing.Versioning{Type: "staggered"}}
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

// readOthers unions the folder lists published by other PCs.
func readOthers(me string) []SharedFolder {
	es, _ := os.ReadDir(Dir())
	seen := map[string]bool{}
	var out []SharedFolder
	for _, e := range es {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, me) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(Dir(), n))
		if err != nil {
			continue
		}
		var df DeviceFile
		if json.Unmarshal(b, &df) != nil {
			continue
		}
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
	es, _ := os.ReadDir(Dir())
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(Dir(), e.Name()))
		if err != nil {
			continue
		}
		var df DeviceFile
		if json.Unmarshal(b, &df) == nil && df.Device != "" {
			m[df.Device] = df.Name
		}
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
