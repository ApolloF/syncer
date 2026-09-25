package meta

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
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
	old := inOneDrive
	inOneDrive = func(p string) bool { return paths.Within(od, p) }
	defer func() { inOneDrive = old }()

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
	}
	for _, c := range cases {
		if _, got := Adoptable(c.sf, c.s, c.installed, synced); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
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
