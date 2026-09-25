package meta

import (
	"path/filepath"
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
	}
	for _, c := range cases {
		if _, got := Adoptable(c.sf, c.s, c.installed); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
