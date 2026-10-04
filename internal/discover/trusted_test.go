package discover

import (
	"path/filepath"
	"testing"

	"github.com/ApolloF/syncer/internal/paths"
)

// A bad edit to the game database can't make auto-add pick a whole root's
// folders, climb out of a root or name a folder another way.
func TestResolveRefusesUntrustedPaths(t *testing.T) {
	home, docs := t.TempDir(), t.TempDir()
	defer paths.SetRootForTest(paths.Home, home)()
	defer paths.SetRootForTest(paths.Documents, docs)()
	for _, d := range []string{`.ssh`, `AppData\Roaming\Microsoft`, `MICROS~1`, `Game\Saves`} {
		writeStoreFile(t, filepath.Join(home, d, "f"), "x")
	}
	writeStoreFile(t, filepath.Join(docs, "My Games", "Game", "s.sav"), "x")
	for _, raw := range []string{
		"<home>/*",
		"<home>/*/Saves",
		"<home>/./*",
		"<home>/.ss?",
		"<winDocuments>/../.ssh",
		"<winDocuments>/My Games/../../.ssh",
		"<home>/MICROS~1",
		"<home>/Game/Saves::$INDEX_ALLOCATION",
		`<home>\*`,
	} {
		if got := resolve(raw, "me", newExistCache()); len(got) != 0 {
			t.Errorf("resolve(%q) = %q", raw, got)
		}
	}
	for raw, want := range map[string]string{
		"<home>/Game/*":                filepath.Join(home, "Game", "Saves"),
		"<winDocuments>/My Games/Game": filepath.Join(docs, "My Games", "Game"),
	} {
		if got := resolve(raw, "me", newExistCache()); len(got) != 1 || got[0] != want {
			t.Errorf("resolve(%q) = %q, want %q", raw, got, want)
		}
	}
}
