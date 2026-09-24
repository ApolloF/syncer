package discover

import (
	"os"
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
	c := es[1]
	if c.Name != "Game: With Colon" || c.SteamCloud || len(c.Paths) != 1 || c.Paths[0] != "<winDocuments>/My Games/Colon/<storeUserId>" {
		t.Errorf("colon: %+v", c)
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
