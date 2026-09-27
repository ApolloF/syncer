package discover

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

const sample = `Stardew Valley:
  cloud:
    gog: true
    steam: true
  files:
    "<home>/.config/StardewValley/Saves":
      tags:
        - save
      when:
        - os: mac
    "<winAppData>/StardewValley/Saves":
      tags:
        - save
      when:
        - os: windows
    "<winAppData>/StardewValley/default_options":
      tags:
        - config
      when:
        - os: windows
  installDir:
    Stardew Valley: {}
    "Other Dir": {}
    'Game: It''s a Dir':
  steam:
    id: 413150
"Game: With Colon":
  files:
    <winDocuments>/My Games/Colon/<storeUserId>:
      tags:
        - save
      when:
        - store: steam
    <base>/saves:
      tags:
        - save
No Files Game:
  steam:
    id: 1
`

func TestParseSample(t *testing.T) {
	es, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(es), es)
	}
	sv := es[0]
	if sv.Name != "Stardew Valley" || !sv.SteamCloud || sv.SteamID != 413150 || len(sv.Paths) != 1 || sv.Paths[0] != "<winAppData>/StardewValley/Saves" {
		t.Errorf("stardew: %+v", sv)
	}
	if sv.SteamID != 413150 || !reflect.DeepEqual(sv.InstallDirs, []string{"Stardew Valley", "Other Dir", "Game: It's a Dir"}) {
		t.Errorf("stardew install metadata: %+v", sv)
	}
	c := es[1]
	if c.Name != "Game: With Colon" || c.SteamCloud || c.SteamID != 0 || len(c.Paths) != 1 || c.Paths[0] != "<winDocuments>/My Games/Colon/<storeUserId>" {
		t.Errorf("colon: %+v", c)
	}
	if c.SteamID != 0 || len(c.InstallDirs) != 0 {
		t.Errorf("install metadata leaked between entries: %+v", c)
	}
}

func TestParseInvalidSteamID(t *testing.T) {
	for _, id := range []string{"nope", "-1", "0", "999999999999999999999999"} {
		es, err := Parse(strings.NewReader("Game:\n  files:\n    <winAppData>/Game:\n  steam:\n    id: " + id + "\n"))
		if err != nil || len(es) != 1 || es[0].SteamID != 0 {
			t.Errorf("id %q: entries=%+v err=%v", id, es, err)
		}
	}
}

func TestCachedManifestMemory(t *testing.T) {
	cacheMu.Lock()
	old := cached
	cached = []Entry{{Name: "Cached Game", SteamID: 123, InstallDirs: []string{"Game"}}}
	cacheMu.Unlock()
	t.Cleanup(func() {
		cacheMu.Lock()
		cached = old
		cacheMu.Unlock()
	})
	es := CachedManifest()
	if len(es) != 1 || es[0].Name != "Cached Game" || es[0].SteamID != 123 {
		t.Fatalf("cached index: %+v", es)
	}
}

// TestParseReal runs against the full manifest when SYNCER_MANIFEST points at it.
func TestParseReal(t *testing.T) {
	p := os.Getenv("SYNCER_MANIFEST")
	if p == "" {
		t.Skip("SYNCER_MANIFEST not set")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	start := time.Now()
	es, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed %d windows entries in %v", len(es), time.Since(start))
	start = time.Now()
	found := Scan(es)
	t.Logf("scan found %d in %v", len(found), time.Since(start))
	for _, g := range found {
		t.Logf("  %-40s cloud=%-5v known=%-5v %s", g.Name, g.SteamCloud, g.Known, g.Path)
	}
}

const ubisoftSample = `---
"Assassin's Creed Odyssey":
  cloud:
    uplay: true
  files:
    "<root>/savegames/<storeUserId>/5059":
      tags:
        - save
      when:
        - store: uplay
    "<root>/savegames/<storeUserId>/5092":
      tags:
        - save
      when:
        - store: steam
        - store: uplay
    "<root>/userdata/<storeUserId>/812140/remote":
      tags:
        - save
      when:
        - store: steam
  steam:
    id: 812140
`

func TestParseUbisoft(t *testing.T) {
	es, err := Parse(strings.NewReader(ubisoftSample))
	if err != nil || len(es) != 1 {
		t.Fatalf("entries=%+v err=%v", es, err)
	}
	e := es[0]
	if want := []string{"<ubisoft>/<storeUserId>/5059", "<ubisoft>/<storeUserId>/5092"}; !reflect.DeepEqual(e.Paths, want) {
		t.Errorf("paths = %q, want %q (Steam's own folder is Steam Cloud's)", e.Paths, want)
	}
	if want := []int{5059, 5092}; !reflect.DeepEqual(e.UbisoftIDs, want) {
		t.Errorf("ubisoft ids = %v, want %v", e.UbisoftIDs, want)
	}
}

func TestResolveUbisoft(t *testing.T) {
	u := t.TempDir()
	defer paths.SetRootForTest(paths.Ubisoft, u)()
	for _, d := range []string{`a1\5092`, `a1\5059`, `b2\5092`, `b2\66088`} {
		writeStoreFile(t, filepath.Join(u, d, "1.save"), "x")
	}
	got := resolve("<ubisoft>/<storeUserId>/5092", "me", newExistCache())
	want := []string{filepath.Join(u, "a1", "5092"), filepath.Join(u, "b2", "5092")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolve = %q, want %q", got, want)
	}
	broad := tooBroad()
	for _, acct := range []string{"a1", "b2"} {
		if !broad[strings.ToLower(filepath.Join(u, acct))] {
			t.Errorf("account folder %s isn't too broad", acct)
		}
	}
}

func TestReadIndexFallsBack(t *testing.T) {
	defer paths.SetRootForTest(paths.Local, t.TempDir())()
	cacheMu.Lock()
	cached = nil
	cacheMu.Unlock()
	if _, err := readIndex(); err == nil {
		t.Fatal("read an index that isn't there")
	}
	es := []Entry{{Name: "Game", Paths: []string{"<winAppData>/Game"}}}
	if err := writeIndex(es); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(indexFile(), filepath.Join(cacheDir(), "manifest-index-v3.gob.gz")); err != nil {
		t.Fatal(err)
	}
	got, err := readIndex()
	if err != nil || !reflect.DeepEqual(got, es) {
		t.Errorf("fallback to the previous index: got %+v, %v", got, err)
	}
}
