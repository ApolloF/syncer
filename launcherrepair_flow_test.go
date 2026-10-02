package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

const markerMissingMsg = "folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)"

// fakeST is just enough of Syncthing's REST API for a repair: a folder list
// it can add to and remove from, and a folder whose marker is missing until
// it is added again.
type fakeST struct {
	mu      sync.Mutex
	folders map[string]map[string]any
	broken  map[string]bool
	failAdd bool
	calls   []string
	onCall  func(call string)
}

func newFakeST(t *testing.T, f map[string]any) (*fakeST, *syncthing.Client) {
	t.Helper()
	st := &fakeST{folders: map[string]map[string]any{f["id"].(string): f}, broken: map[string]bool{f["id"].(string): true}}
	srv := httptest.NewServer(http.HandlerFunc(st.serve))
	t.Cleanup(srv.Close)
	c, err := syncthing.NewAt(strings.TrimPrefix(srv.URL, "http://"), "key")
	if err != nil {
		t.Fatal(err)
	}
	return st, c
}

func (s *fakeST) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	if s.onCall != nil {
		s.onCall(call)
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case call == "GET /rest/system/status":
		reply(map[string]any{"myID": "ME"})
	case call == "GET /rest/config/devices":
		reply([]map[string]any{{"deviceID": "ME"}, {"deviceID": "OTHER"}})
	case call == "GET /rest/config/folders":
		var out []map[string]any
		for _, f := range s.folders {
			out = append(out, f)
		}
		reply(out)
	case call == "POST /rest/config/folders":
		var f map[string]any
		_ = json.NewDecoder(r.Body).Decode(&f)
		if s.failAdd && f["id"] != "syncer-meta" {
			http.Error(w, "something went wrong", http.StatusBadRequest)
			return
		}
		s.calls = append(s.calls, "add "+f["id"].(string))
		s.folders[f["id"].(string)] = f
		delete(s.broken, f["id"].(string))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/rest/config/folders/"):
		id := strings.TrimPrefix(r.URL.Path, "/rest/config/folders/")
		s.calls = append(s.calls, "remove "+id)
		delete(s.folders, id)
	case call == "GET /rest/db/status":
		id := r.URL.Query().Get("folder")
		if _, ok := s.folders[id]; !ok {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		if s.broken[id] {
			reply(map[string]any{"state": "error", "error": markerMissingMsg})
			return
		}
		reply(map[string]any{"state": "scanning"})
	case call == "GET /rest/folder/errors":
		reply(map[string]any{"errors": []any{}})
	case r.Method == http.MethodPatch:
	default:
		reply(map[string]any{})
	}
}

// repairSetup is a launcher data folder whose marker went missing after
// the launcher started over, with a backup from before.
func repairSetup(t *testing.T) (profile, target string, f syncthing.Folder) {
	t.Helper()
	t.Cleanup(paths.SetRootForTest(paths.Roaming, t.TempDir())) // Syncer's own settings and state
	t.Cleanup(paths.SetRootForTest(paths.Local, t.TempDir()))   // snapshots made without a backup
	profile, target = filepath.Join(t.TempDir(), "Profile"), t.TempDir()
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.BackupRoot = target
		s.Launchers["seaglass-profile"] = "Seaglass"
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	writeAt(t, filepath.Join(target, "seaglass-profile", "snig77", "PC-Other.json"), "other pc", old)
	writeAt(t, filepath.Join(target, "seaglass-profile", "snig77", "PC-Here.json"), "old playtime", old)
	writeAt(t, filepath.Join(profile, "snig77", "PC-Here.json"), "new start", time.Now())
	return profile, target, syncthing.Folder{ID: "seaglass-profile", Label: "Seaglass (playtime, achievements, settings)", Path: profile}
}

func TestRepairRestoresThenRejoinsUnderTheSameID(t *testing.T) {
	profile, _, f := repairSetup(t)
	st, c := newFakeST(t, map[string]any{"id": f.ID, "label": f.Label, "path": f.Path})
	st.onCall = func(call string) {
		if strings.HasPrefix(call, "DELETE ") {
			// Taken out of Syncthing only once the backup's files are back.
			if _, err := os.Stat(filepath.Join(profile, "snig77", "PC-Other.json")); err != nil {
				t.Errorf("removed before restoring: %v", err)
			}
		}
		if call == "POST /rest/config/folders" {
			if _, err := os.Stat(filepath.Join(profile, ".stfolder")); err == nil {
				t.Error("Syncer made the marker itself; Syncthing must make it for a fresh folder")
			}
		}
	}
	r, err := repairLauncherData(context.Background(), c, f, "Seaglass")
	if err != nil || !r.Done {
		t.Fatalf("repair: %+v, %v", r, err)
	}
	if r.Restored != 1 || r.Beside != 1 {
		t.Errorf("restored %d, beside %d; want 1 and 1", r.Restored, r.Beside)
	}
	if got := strings.Join(st.calls, ", "); !strings.HasPrefix(got, "remove seaglass-profile, add seaglass-profile") {
		t.Errorf("calls = %s", got)
	}
	added := st.folders[f.ID]
	if added["label"] != f.Label || added["path"] != f.Path {
		t.Errorf("added back as %v", added)
	}
	if devs, _ := json.Marshal(added["devices"]); !strings.Contains(string(devs), "OTHER") {
		t.Errorf("not shared with the other PC: %s", devs)
	}
	for rel, want := range map[string]string{
		filepath.Join("snig77", "PC-Other.json"): "other pc",
		filepath.Join("snig77", "PC-Here.json"):  "new start",
	} {
		if b, _ := os.ReadFile(filepath.Join(profile, rel)); string(b) != want {
			t.Errorf("%s = %q, want %q", rel, b, want)
		}
	}
	beside, _ := filepath.Glob(filepath.Join(profile, "snig77", "PC-Here.restored-*.json"))
	if len(beside) != 1 {
		t.Fatalf("restored copies next to PC-Here.json: %v", beside)
	}
	if b, _ := os.ReadFile(beside[0]); string(b) != "old playtime" {
		t.Errorf("restored copy = %q", b)
	}
	if len(store.LoadState().Rejoin) != 0 {
		t.Error("still waiting to be added back")
	}

	// Once is enough: a folder that's fine is left alone.
	st.calls = nil
	if repairLaunchers(context.Background(), c) || len(st.calls) != 0 {
		t.Errorf("repaired a folder that's fine: %v", st.calls)
	}
}

func TestRepairThatCouldNotAddBackFinishesLater(t *testing.T) {
	_, _, f := repairSetup(t)
	st, c := newFakeST(t, map[string]any{"id": f.ID, "label": f.Label, "path": f.Path})
	st.failAdd = true
	if _, err := repairLauncherData(context.Background(), c, f, "Seaglass"); err == nil {
		t.Fatal("no error although adding it back failed")
	}
	if _, ok := store.LoadState().Rejoin[f.ID]; !ok {
		t.Fatal("not remembered for adding back")
	}
	// Not repaired again right away: it's added back as it is.
	st.mu.Lock()
	st.failAdd, st.calls = false, nil
	st.mu.Unlock()
	if !repairLaunchers(context.Background(), c) {
		t.Fatal("not added back")
	}
	if got := strings.Join(st.calls, ", "); !strings.HasPrefix(got, "add seaglass-profile") || strings.Contains(got, "remove") {
		t.Errorf("calls = %s", got)
	}
	if len(store.LoadState().Rejoin) != 0 {
		t.Error("still waiting to be added back")
	}
}

func TestRepairWithoutABackupStillRejoins(t *testing.T) {
	_, _, f := repairSetup(t)
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.BackupRoot = filepath.Join(t.TempDir(), "gone", "deeper") }); err != nil {
		t.Fatal(err)
	}
	st, c := newFakeST(t, map[string]any{"id": f.ID, "label": f.Label, "path": f.Path})
	r, err := repairLauncherData(context.Background(), c, f, "Seaglass")
	if err != nil || !r.Done || r.NoBackup == "" {
		t.Fatalf("repair: %+v, %v", r, err)
	}
	if got := strings.Join(st.calls, ", "); !strings.HasPrefix(got, "remove seaglass-profile, add seaglass-profile") {
		t.Errorf("calls = %s", got)
	}
}

func TestRepairNowDoesNotWaitOutAnEarlierRepair(t *testing.T) {
	profile, _, f := repairSetup(t)
	st, c := newFakeST(t, map[string]any{"id": f.ID, "label": f.Label, "path": f.Path})
	if _, err := repairNow(context.Background(), c, "some-game"); err == nil {
		t.Error("repaired a folder that isn't a launcher's data")
	}
	// Repaired an hour ago, and emptied again since.
	store.UpdateState(func(s *store.State) { s.Repaired = map[string]time.Time{f.ID: time.Now().Add(-time.Hour)} })
	if repairLaunchers(context.Background(), c) {
		t.Fatal("the background run didn't wait")
	}
	r, err := repairNow(context.Background(), c, f.ID)
	if err != nil || !r.Done {
		t.Fatalf("repair now: %+v, %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(profile, "snig77", "PC-Other.json")); err != nil {
		t.Errorf("not restored: %v", err)
	}
	if got := strings.Join(st.calls, ", "); !strings.HasPrefix(got, "remove seaglass-profile, add seaglass-profile") {
		t.Errorf("calls = %s", got)
	}
	if _, err := repairNow(context.Background(), c, f.ID); err == nil || !strings.Contains(err.Error(), "doesn't need") {
		t.Errorf("repairing a folder that's fine: %v", err)
	}
}
