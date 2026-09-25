package discover

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if sv.Name != "Stardew Valley" || !sv.SteamCloud || len(sv.Paths) != 1 || sv.Paths[0] != "<winAppData>/StardewValley/Saves" {
		t.Errorf("stardew: %+v", sv)
	}
	if sv.SteamID != 413150 || !reflect.DeepEqual(sv.InstallDirs, []string{"Stardew Valley", "Other Dir", "Game: It's a Dir"}) {
		t.Errorf("stardew install metadata: %+v", sv)
	}
	c := es[1]
	if c.Name != "Game: With Colon" || c.SteamCloud || len(c.Paths) != 1 || c.Paths[0] != "<winDocuments>/My Games/Colon/<storeUserId>" {
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
