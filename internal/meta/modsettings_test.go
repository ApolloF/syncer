package meta

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func TestModSettingsNewestWins(t *testing.T) {
	defer paths.SetRootForTest(paths.Roaming, t.TempDir())()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		s.FindMods, s.ModsMaxGB, s.ModsChanged = true, 20, t0
	}); err != nil {
		t.Fatal(err)
	}
	peer := func(m *ModSettings) {
		if err := store.WriteJSON(filepath.Join(Dir(), "PEER.json"), DeviceFile{Device: "PEER", Name: "Laptop", Mods: m}); err != nil {
			t.Fatal(err)
		}
	}
	var took []store.Settings
	ModSettingsAdopted = func(_ context.Context, _ *syncthing.Client, old, now store.Settings) { took = append(took, now) }
	defer func() { ModSettingsAdopted = nil }()

	// An older PC publishes nothing: nothing changes, nothing differs.
	peer(nil)
	adoptModSettings(context.Background(), nil, "ME")
	if len(took) != 0 || len(ModSettingsDiffer("ME")) != 0 {
		t.Fatal("a PC without shared mod settings changed or differed")
	}

	// Different but older: kept here, shown as differing.
	peer(&ModSettings{FindMods: true, ShareVortexMods: true, ModsMaxGB: 20, Changed: t0.Add(-time.Hour)})
	adoptModSettings(context.Background(), nil, "ME")
	if len(took) != 0 {
		t.Fatal("older mod settings were taken over")
	}
	if d := ModSettingsDiffer("ME"); len(d) != 1 || d[0] != "Laptop" {
		t.Fatalf("differ = %v, want [Laptop]", d)
	}

	// Newer: taken over, including the stamp (so it isn't taken again).
	peer(&ModSettings{FindMods: true, ShareVortexMods: true, ModsMaxGB: -1, Changed: t0.Add(time.Hour)})
	adoptModSettings(context.Background(), nil, "ME")
	s := store.LoadSettings()
	if len(took) != 1 || !s.ShareVortexMods || s.ModsMaxGB != -1 || !s.ModsChanged.Equal(t0.Add(time.Hour)) {
		t.Fatalf("newer mod settings not taken over: %+v", ModSettingsOf(s))
	}
	adoptModSettings(context.Background(), nil, "ME")
	if len(took) != 1 || len(ModSettingsDiffer("ME")) != 0 {
		t.Fatal("the same mod settings were taken over twice, or still differ")
	}

	// A change here is stamped after the newest any PC published, even
	// when that PC's clock runs ahead.
	peer(&ModSettings{FindMods: true, Changed: time.Now().Add(time.Hour)})
	if !NewModStamp().After(time.Now().Add(time.Hour - time.Minute)) {
		t.Fatal("a change here would lose against a PC whose clock runs ahead")
	}

	// Options that build on finding mods go off with it.
	var off store.Settings
	(ModSettings{FindMods: false, ShareVortexMods: true, AutoAddMods: true}).Apply(&off)
	if off.ShareVortexMods || off.AutoAddMods {
		t.Fatal("mod options stayed on without finding mods")
	}
}
