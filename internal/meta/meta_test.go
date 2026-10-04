package meta

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func TestAdoptable(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	s := store.Settings{Ignored: map[string]bool{"gone": true},
		BackupOnly: map[string]store.LocalFolder{"bo": {ID: "bo"},
			"g--pc": {ID: "g--pc", Path: filepath.Join(paths.Root(paths.Roaming), "Local"), SyncID: "was-synced"}}}
	only := s
	only.InstalledOnly = true
	game := SharedFolder{ID: "stardew", Label: "Stardew Valley", Root: paths.Roaming, Rel: "StardewValley/Saves"}
	with := func(id, root, rel string) SharedFolder { return SharedFolder{ID: id, Label: id, Root: root, Rel: rel} }
	roaming := paths.Root(paths.Roaming)
	synced := []string{filepath.Join(roaming, `Arrowhead\Helldivers2\saves`), filepath.Join(roaming, "Mobius")}
	od := filepath.Join(roaming, "OneDriveHere")
	old, oldClassify := inOneDrive, classify
	inOneDrive = func(p string) bool { return paths.Within(od, p) }
	classify = func(label, p string) discover.Class {
		switch label {
		case "steam":
			return discover.Class{SteamCloud: true}
		case "copy":
			return discover.Class{CopyOf: "Game"}
		}
		return discover.Class{}
	}
	defer func() { inOneDrive, classify = old, oldClassify }()
	copied := with("bg3-rune", paths.Public, "Documents/Steam/RUNE/1086940")
	copied.CopyOf = "Baldur's Gate 3"

	cases := []struct {
		name      string
		sf        SharedFolder
		s         store.Settings
		installed func(string) bool
		want      string
	}{
		{"ok", game, s, no, ""},
		{"installed only, installed", game, only, yes, ""},
		{"installed only, missing", game, only, no, SkipNotInstalled},
		{"ignored", with("gone", paths.Roaming, "Gone"), s, yes, SkipRemoved},
		{"backup only", with("bo", paths.Roaming, "Bo"), s, yes, SkipBackupOnly},
		{"backup only by path", with("other", paths.Roaming, "Local/Game"), s, yes, SkipBackupOnly},
		{"backup only by sync id", with("was-synced", paths.Roaming, "Elsewhere"), s, yes, SkipBackupOnly},
		{"bad id", with(`..\x`, paths.Roaming, "X"), s, yes, SkipUnsafe},
		{"meta id", with(FolderID, paths.Roaming, "X"), s, yes, SkipUnsafe},
		{"traversal", with("x", paths.Roaming, "../../.ssh"), s, yes, SkipUnsafe},
		{"whole profile", with("x", paths.Home, ""), s, yes, SkipUnsafe},
		{"syncthing config", with("x", paths.Local, "Syncthing"), s, yes, SkipUnsafe},
		{"startup folder", with("x", paths.Roaming, "Microsoft/Windows/Start Menu/Programs/Startup"), s, yes, SkipUnsafe},
		{"unknown root", with("x", "system32", "x"), s, yes, SkipUnsafe},
		{"inside a synced folder", with("inner", paths.Roaming, "Mobius/Outer Wilds"), s, yes, SkipOverlap},
		{"holds a synced folder", with("outer", paths.Roaming, "Arrowhead"), s, yes, SkipOverlap},
		{"same path as a synced folder", with("dup", paths.Roaming, "Arrowhead/Helldivers2/saves"), s, yes, SkipOverlap},
		{"next to a synced folder", with("sib", paths.Roaming, "MobiusX"), s, yes, ""},
		{"in OneDrive", with("od", paths.Roaming, "OneDriveHere/Game"), s, yes, SkipOneDrive},
		{"Steam Cloud keeps it here", with("steam", paths.Roaming, "Steam/Saves"), s, yes, SkipSteamCloud},
		{"not installed beats Steam Cloud", with("steam", paths.Roaming, "Steam/Saves"), only, no, SkipNotInstalled},
		{"emulator copy, found here", with("copy", paths.Roaming, "Copy"), s, yes, SkipCopy},
		{"emulator copy, says the other PC", copied, s, yes, SkipCopy},
	}
	for _, c := range cases {
		if _, got := Adoptable(c.sf, c.s, c.installed, synced); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAdoptableUbisoft(t *testing.T) {
	no := func(string) bool { return false }
	odyssey := SharedFolder{ID: "aco", Label: "Assassin's Creed Odyssey", Root: paths.Ubisoft, Rel: "0a1b2c/5092"}
	account := SharedFolder{ID: "acct", Label: "acct", Root: paths.Ubisoft, Rel: "0a1b2c"}

	restore := paths.SetRootForTest(paths.Ubisoft, "")
	if _, got := Adoptable(odyssey, store.Settings{}, no, nil); got != SkipNoRoot {
		t.Errorf("no Ubisoft Connect here: got %q, want %q", got, SkipNoRoot)
	}
	restore()

	defer paths.SetRootForTest(paths.Ubisoft, `C:\Ubisoft Test\savegames`)() // not below Temp, which is never syncable
	if _, got := Adoptable(odyssey, store.Settings{}, no, nil); got != SkipUbisoftCloud {
		t.Errorf("Ubisoft Connect's folder: got %q, want %q", got, SkipUbisoftCloud)
	}
	if _, got := Adoptable(account, store.Settings{}, no, nil); got != SkipUnsafe {
		t.Errorf("a whole Ubisoft account: got %q, want %q", got, SkipUnsafe)
	}
}

func TestDropOuter(t *testing.T) {
	av := func(id, p string) Avail { return Avail{SharedFolder: SharedFolder{ID: id}, Path: p} }
	got := dropOuter([]Avail{
		av("roaming-arrowhead", `C:\R\Arrowhead`),
		av("helldivers-2", `C:\R\Arrowhead\Helldivers2\saves`),
		av("other", `C:\R\ArrowheadX`),
		av("first", `C:\R\Same`),
		av("second", `c:\r\same\`),
	})
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if want := "helldivers-2 other first"; strings.Join(ids, " ") != want {
		t.Fatalf("got %v, want %s", ids, want)
	}
}

func TestPatchFor(t *testing.T) {
	fd := func(ids ...string) []syncthing.FolderDevice {
		var out []syncthing.FolderDevice
		for _, id := range ids {
			out = append(out, syncthing.FolderDevice{DeviceID: id})
		}
		return out
	}
	done := syncthing.Folder{ID: "game", Devices: fd("me", "b"), Versioning: syncthing.Versioning{Type: "staggered"}, MaxConflicts: -1}
	if p := patchFor(done, "me", []string{"b"}); len(p) != 0 {
		t.Errorf("nothing to change: %v", p)
	}
	old := syncthing.Folder{ID: "game", Devices: fd("me"), MaxConflicts: 10}
	p := patchFor(old, "me", []string{"c", "b"})
	if got := devIDs(p["devices"]); got != "me b c" {
		t.Errorf("devices = %q", got)
	}
	if p["maxConflicts"] != -1 || p["versioning"] == nil {
		t.Errorf("conflicts/versioning not fixed: %v", p)
	}
	m := patchFor(syncthing.Folder{ID: FolderID, Devices: fd("me", "b")}, "me", []string{"b"})
	if len(m) != 0 {
		t.Errorf("metadata folder needs no versioning or conflict copies: %v", m)
	}
}

func devIDs(v any) string {
	var ids []string
	for _, d := range v.([]map[string]string) {
		ids = append(ids, d["deviceID"])
	}
	return strings.Join(ids, " ")
}

// A paired PC publishes a folder whose path spells something harmless but
// leads, through a junction Windows keeps in every profile, a short name or a
// stream, to Startup, ~/.ssh or Syncthing's keys: it's never adopted.
func TestAdoptableRefusesAliasedPaths(t *testing.T) {
	cases := []SharedFolder{
		{Root: paths.Home, Rel: "Start Menu/Programs/Startup"},
		{Root: paths.Home, Rel: "Application Data/Microsoft/Windows/Start Menu/Programs/Startup"},
		{Root: paths.Home, Rel: "Local Settings/Syncthing"},
		{Root: paths.Home, Rel: "SSH~1"},
		{Root: paths.Local, Rel: "SYNCTH~1"},
		{Root: paths.Roaming, Rel: "MICROS~1/Windows/Start Menu/Programs/Startup"},
		{Root: paths.Roaming, Rel: "Microsoft::$INDEX_ALLOCATION/Windows/Start Menu/Programs/Startup"},
	}
	for i, sf := range cases {
		sf.ID, sf.Label = "alias-"+string(rune('a'+i)), "Game"
		if !strings.ContainsAny(sf.Rel, "~:") {
			if p, _ := paths.Resolve(sf.Root, sf.Rel); !exists(p) {
				continue // not in this profile
			}
		}
		if p, reason := Adoptable(sf, store.Settings{}, func(string) bool { return true }, nil); reason != SkipUnsafe {
			t.Errorf("%s/%s: adopted as %q (reason %q), want %q", sf.Root, sf.Rel, p, reason, SkipUnsafe)
		}
	}
}

// A folder this PC reaches through a junction (moved to another drive) is
// offered with the reason it isn't synced here, not dropped as unsafe.
func TestAdoptableLinkSaysSo(t *testing.T) {
	base := t.TempDir()
	roaming := filepath.Join(base, "roaming")
	moved := filepath.Join(base, "other drive", "Game")
	for _, d := range []string{roaming, filepath.Join(moved, "Saves")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer paths.SetRootForTest(paths.Roaming, roaming)()
	defer paths.SetRootForTest(paths.Local, filepath.Join(base, "local"))() // the real one holds Temp
	link := filepath.Join(roaming, "Game")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, moved).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction: %v %s", err, out)
	}
	sf := SharedFolder{ID: "linked", Label: "Game", Root: paths.Roaming, Rel: "Game/Saves"}
	p, reason := Adoptable(sf, store.Settings{}, func(string) bool { return true }, nil)
	if reason != SkipLink || p != filepath.Join(link, "Saves") {
		t.Errorf("Adoptable = %q, %q; want %q, %q", p, reason, filepath.Join(link, "Saves"), SkipLink)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return p != "" && err == nil
}
