package meta

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func devs(ids ...string) []syncthing.FolderDevice {
	var out []syncthing.FolderDevice
	for _, id := range ids {
		out = append(out, syncthing.FolderDevice{DeviceID: id})
	}
	return out
}

// states answers remote states from a table keyed "folder/device".
func states(m map[string]string) func(folder, device string) string {
	return func(folder, device string) string {
		if s, ok := m[folder+"/"+device]; ok {
			return s
		}
		return syncthing.RemoteUnknown
	}
}

func TestWhose(t *testing.T) {
	const (
		valid, notSharing = syncthing.RemoteValid, syncthing.RemoteNotSharing
	)
	pcs := map[string]bool{"LEGION": true}
	direct := map[string]bool{"LEGION": true, "DOCKERD": true, "CLAUDE": true, "NAS": true}
	cases := []struct {
		name      string
		f         syncthing.Folder
		strong    bool
		published bool
		st        map[string]string
		want      string
	}{
		{"notes vault with a server", syncthing.Folder{ID: "notes", Devices: devs("ME", "DOCKERD", "CLAUDE", "LEGION")}, false, false,
			map[string]string{"notes/DOCKERD": valid, "notes/CLAUDE": valid, "notes/LEGION": notSharing}, ownerOther},
		{"a server accepted it, even though another PC publishes it", syncthing.Folder{ID: "notes", Devices: devs("ME", "DOCKERD")}, false, true,
			map[string]string{"notes/DOCKERD": valid}, ownerOther},
		{"game offered to the server", syncthing.Folder{ID: "game", Devices: devs("ME", "LEGION", "DOCKERD")}, false, false,
			map[string]string{"game/LEGION": valid, "game/DOCKERD": notSharing}, ownerSyncer},
		{"game also on a NAS", syncthing.Folder{ID: "game", Devices: devs("ME", "LEGION", "NAS")}, false, false,
			map[string]string{"game/LEGION": valid, "game/NAS": valid}, ownerSyncer},
		{"other PC offline, published", syncthing.Folder{ID: "game", Devices: devs("ME", "LEGION", "DOCKERD")}, false, true,
			map[string]string{"game/DOCKERD": notSharing}, ownerSyncer},
		{"server offline: wait", syncthing.Folder{ID: "x", Devices: devs("ME", "DOCKERD")}, false, false, nil, ownerUnknown},
		{"server offline, published", syncthing.Folder{ID: "x", Devices: devs("ME", "DOCKERD")}, false, true, nil, ownerSyncer},
		{"only offered to the server", syncthing.Folder{ID: "x", Devices: devs("ME", "DOCKERD")}, false, false,
			map[string]string{"x/DOCKERD": notSharing}, ownerSyncer},
		{"introduced device offline", syncthing.Folder{ID: "x", Devices: devs("ME", "INTRODUCED")}, false, false, nil, ownerSyncer},
		{"this PC only", syncthing.Folder{ID: "x", Devices: devs("ME")}, false, false, nil, ownerSyncer},
		{"named in Syncer's settings", syncthing.Folder{ID: "mods", Devices: devs("ME", "DOCKERD")}, true, false,
			map[string]string{"mods/DOCKERD": valid}, ownerSyncer},
	}
	for _, c := range cases {
		if got := whose(c.f, "ME", pcs, direct, c.strong, c.published, states(c.st)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUntangled(t *testing.T) {
	pcs := map[string]bool{"LEGION": true}
	introduced := map[string]bool{"DOCKERD": true}
	ids := func(fds []syncthing.FolderDevice) string {
		var out []string
		for _, d := range fds {
			out = append(out, d.DeviceID)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name       string
		f          syncthing.Folder
		own        bool
		introduced map[string]bool
		st         map[string]string
		want       string
	}{
		{"game: the server never accepted it", syncthing.Folder{ID: "g", Devices: devs("ME", "LEGION", "DOCKERD")}, true, nil,
			map[string]string{"g/DOCKERD": syncthing.RemoteNotSharing, "g/LEGION": syncthing.RemoteNotSharing}, "ME,LEGION"},
		{"game: a NAS that syncs it keeps it", syncthing.Folder{ID: "g", Devices: devs("ME", "LEGION", "NAS")}, true, nil,
			map[string]string{"g/NAS": syncthing.RemoteValid}, "ME,LEGION,NAS"},
		{"game: offline device stays", syncthing.Folder{ID: "g", Devices: devs("ME", "NAS")}, true, nil, nil, "ME,NAS"},
		{"game: introduced by a Syncer PC, can't connect", syncthing.Folder{ID: "g", Devices: devs("ME", "LEGION", "DOCKERD")}, true,
			introduced, nil, "ME,LEGION"},
		{"notes: the other PC never accepted it", syncthing.Folder{ID: "n", Devices: devs("ME", "DOCKERD", "LEGION")}, false, nil,
			map[string]string{"n/DOCKERD": syncthing.RemoteValid, "n/LEGION": syncthing.RemoteNotSharing}, "ME,DOCKERD"},
		{"notes: the other PC syncs it", syncthing.Folder{ID: "n", Devices: devs("ME", "DOCKERD", "LEGION")}, false, nil,
			map[string]string{"n/LEGION": syncthing.RemoteValid}, "ME,DOCKERD,LEGION"},
	}
	for _, c := range cases {
		if got := ids(untangled(c.f, "ME", c.own, pcs, c.introduced, states(c.st))); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestOwnDevicesAndFolders(t *testing.T) {
	defer paths.SetRootForTest(paths.Roaming, t.TempDir())()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(Dir(), "LEGION.json"), DeviceFile{Device: "LEGION"}); err != nil {
		t.Fatal(err)
	}
	MarkSyncerPC("NEW")
	ds := []syncthing.Device{{DeviceID: "ME"}, {DeviceID: "LEGION"}, {DeviceID: "NEW"}, {DeviceID: "DOCKERD"}}
	var got []string
	for _, d := range OwnDevices("ME", ds) {
		got = append(got, d.DeviceID)
	}
	if strings.Join(got, ",") != "ME,LEGION,NEW" {
		t.Errorf("devices %v, want this PC, the one with a folder list and the one paired in Syncer", got)
	}
	ForgetSyncerPC("NEW")
	if n := len(OwnDevices("ME", ds)); n != 2 {
		t.Errorf("%d devices after forgetting one, want 2", n)
	}

	store.UpdateState(func(st *store.State) { st.OtherFolders = map[string]bool{"notes": true} })
	fs := OwnFolders([]syncthing.Folder{{ID: "game"}, {ID: "notes"}})
	if len(fs) != 1 || fs[0].ID != "game" {
		t.Errorf("folders %+v, want the game only", fs)
	}
	if err := AddFolderSpec(context.Background(), nil, FolderSpec{ID: "notes", Path: t.TempDir()}, "ME", nil); err == nil {
		t.Error("added a folder over one that isn't Syncer's")
	}
}

func TestWriteVortexListsLeavesAnUnchangedFileAlone(t *testing.T) {
	defer paths.SetRootForTest(paths.Roaming, t.TempDir())()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	l := mods.ShareList{"skyrimse": {"m1": {At: time.Now().UnixNano()}}}
	if err := WriteVortexLists("ME", l); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(vortexListFile("ME"), old, old); err != nil {
		t.Fatal(err)
	}
	stamp := VortexListStamp("ME")
	if err := WriteVortexLists("ME", l); err != nil {
		t.Fatal(err)
	}
	if VortexListStamp("ME") != stamp {
		t.Error("the same list was written again: the other PCs take it for a new one")
	}
	l["skyrimse"]["m2"] = mods.ShareEntry{At: time.Now().UnixNano()}
	if err := WriteVortexLists("ME", l); err != nil {
		t.Fatal(err)
	}
	if VortexListStamp("ME") == stamp {
		t.Error("a changed list wasn't written")
	}
}

// fakeSyncthing is a Syncthing that, besides Syncer's PCs, syncs a notes
// vault with a server and a laptop, as one paired before Syncer was fixed:
// every folder offered to every device.
type fakeSyncthing struct {
	mu      sync.Mutex
	folders map[string]syncthing.Folder
	devices map[string]syncthing.Device
	remote  map[string]string // folder/device -> remote state
	pending map[string]any
	patches []string
}

func (f *fakeSyncthing) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	q := r.URL.Query()
	switch call := r.Method + " " + r.URL.Path; {
	case call == "GET /rest/system/status":
		reply(map[string]any{"myID": "ME"})
	case call == "GET /rest/config/devices":
		var out []syncthing.Device
		for _, id := range slices.Sorted(maps.Keys(f.devices)) {
			out = append(out, f.devices[id])
		}
		reply(out)
	case call == "GET /rest/config/folders":
		var out []syncthing.Folder
		for _, id := range slices.Sorted(maps.Keys(f.folders)) {
			out = append(out, f.folders[id])
		}
		reply(out)
	case call == "POST /rest/config/folders":
		var nf syncthing.Folder
		_ = json.NewDecoder(r.Body).Decode(&nf)
		f.folders[nf.ID] = nf
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/rest/config/folders/"):
		id := strings.TrimPrefix(r.URL.Path, "/rest/config/folders/")
		var p struct {
			Devices *[]syncthing.FolderDevice `json:"devices"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.patches = append(f.patches, id)
		if p.Devices != nil {
			fo := f.folders[id]
			fo.Devices = *p.Devices
			f.folders[id] = fo
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/config/devices/"):
		reply(f.devices[strings.TrimPrefix(r.URL.Path, "/rest/config/devices/")])
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/rest/config/devices/"):
		id := strings.TrimPrefix(r.URL.Path, "/rest/config/devices/")
		var p struct {
			IgnoredFolders []syncthing.ObservedFolder `json:"ignoredFolders"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		d := f.devices[id]
		d.IgnoredFolders = p.IgnoredFolders
		f.devices[id] = d
	case call == "GET /rest/cluster/pending/folders":
		reply(f.pending)
	case call == "GET /rest/db/completion":
		rs, ok := f.remote[q.Get("folder")+"/"+q.Get("device")]
		if !ok {
			rs = syncthing.RemoteUnknown
		}
		reply(map[string]any{"completion": 100, "remoteState": rs})
	default:
		reply(map[string]any{})
	}
}

func TestReconcileLeavesOtherDevicesAndFoldersAlone(t *testing.T) {
	defer paths.SetRootForTest(paths.Roaming, t.TempDir())()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// The laptop runs Syncer and publishes The Sims 4, which is backed up
	// only on this PC.
	sims := SharedFolder{ID: "the-sims-4", Label: "The Sims 4", Root: paths.Documents, Rel: "Electronic Arts/The Sims 4/saves"}
	if err := store.WriteJSON(filepath.Join(Dir(), "LEGION.json"), DeviceFile{Device: "LEGION", Name: "Legion",
		Folders: []SharedFolder{sims}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.BackupOnly["the-sims-4--pc"] = store.LocalFolder{ID: "the-sims-4--pc", Label: "The Sims 4", SyncID: "the-sims-4"}
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSyncthing{
		devices: map[string]syncthing.Device{"ME": {DeviceID: "ME"}, "LEGION": {DeviceID: "LEGION"},
			"DOCKERD": {DeviceID: "DOCKERD"}, "CLAUDE": {DeviceID: "CLAUDE"}},
		folders: map[string]syncthing.Folder{
			FolderID: {ID: FolderID, Path: Dir(), Devices: devs("ME", "LEGION", "DOCKERD", "CLAUDE")},
			"game": {ID: "game", Label: "Game", Path: filepath.Join(t.TempDir(), "Game"), Devices: devs("ME", "LEGION", "DOCKERD", "CLAUDE"),
				Versioning: syncthing.Versioning{Type: "staggered"}, MaxConflicts: -1},
			"notes": {ID: "notes", Label: "Notes", Path: filepath.Join(t.TempDir(), "Notes"), Devices: devs("ME", "DOCKERD", "CLAUDE", "LEGION")},
		},
		remote: map[string]string{
			FolderID + "/LEGION": syncthing.RemoteValid, FolderID + "/DOCKERD": syncthing.RemoteNotSharing, FolderID + "/CLAUDE": syncthing.RemoteNotSharing,
			"game/LEGION": syncthing.RemoteValid, "game/DOCKERD": syncthing.RemoteNotSharing, "game/CLAUDE": syncthing.RemoteNotSharing,
			"notes/DOCKERD": syncthing.RemoteValid, "notes/CLAUDE": syncthing.RemoteValid, "notes/LEGION": syncthing.RemoteNotSharing,
		},
		pending: map[string]any{"the-sims-4": map[string]any{"offeredBy": map[string]any{"LEGION": map[string]any{"label": "The Sims 4"}}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer srv.Close()
	c, err := syncthing.NewAt(strings.TrimPrefix(srv.URL, "http://"), "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Reconcile(ctx, c); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	for id, want := range map[string]string{FolderID: "ME,LEGION", "game": "ME,LEGION", "notes": "ME,DOCKERD,CLAUDE"} {
		var got []string
		for _, d := range fake.folders[id].Devices {
			got = append(got, d.DeviceID)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s shared with %v, want %s", id, got, want)
		}
	}
	if fake.folders["notes"].Versioning.Type != "" || fake.folders["notes"].MaxConflicts != 0 {
		t.Errorf("Syncer changed the notes folder's settings: %+v", fake.folders["notes"])
	}
	ign := fake.devices["LEGION"].IgnoredFolders
	if len(ign) != 1 || ign[0].ID != "the-sims-4" {
		t.Errorf("the laptop's offer of a game backed up only here isn't ignored: %+v", ign)
	}
	fake.patches = nil
	fake.mu.Unlock()

	var mine DeviceFile
	if b, err := os.ReadFile(filepath.Join(Dir(), "ME.json")); err != nil || json.Unmarshal(b, &mine) != nil {
		t.Fatalf("own folder list: %v", err)
	}
	for _, sf := range mine.Folders {
		if sf.ID == "notes" {
			t.Error("the notes folder was published to the other PCs")
		}
	}

	defer func(f func([]syncthing.Folder) []syncthing.Folder, d func(string, []syncthing.Device) []syncthing.Device) {
		syncthing.FolderFilter, syncthing.DeviceFilter = f, d
	}(syncthing.FolderFilter, syncthing.DeviceFilter)
	syncthing.FolderFilter, syncthing.DeviceFilter = OwnFolders, OwnDevices
	fs, _ := c.Folders(ctx)
	ds, _ := c.Devices(ctx)
	if len(fs) != 2 || len(ds) != 2 {
		t.Errorf("Syncer sees folders %+v and devices %+v; want its own two of each", fs, ds)
	}

	// Once untangled, nothing changes any more.
	if _, err := Reconcile(ctx, c); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.patches) > 0 {
		t.Errorf("changed %v again", fake.patches)
	}
}
