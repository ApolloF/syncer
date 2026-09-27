package meta

import (
	"errors"
	"testing"

	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func TestAdoptableMods(t *testing.T) {
	const staging, profiles, deployed = `D:\Vortex Mods\skyrimse`, `C:\Users\u\AppData\Roaming\Vortex\skyrimse\profiles`, `D:\Games\Skyrim\Data`
	oldResolve, oldCheck, oldHere := resolveMod, checkMod, deploysHere
	resolveMod = func(root, rel string) (string, error) {
		switch root + "/" + rel {
		case "vortex:skyrimse/staging":
			return staging, nil
		case "vortex:skyrimse/profiles":
			return profiles, nil
		case "game:skyrimse/Data":
			return deployed, nil
		case "vortex:bad/staging":
			return `C:\`, nil
		}
		return "", mods.ErrGameMissing
	}
	checkMod = func(kind, p string) error {
		if p == `C:\` {
			return errors.New("drive")
		}
		return nil
	}
	here := false
	deploysHere = func(string) bool { return here }
	defer func() { resolveMod, checkMod, deploysHere = oldResolve, oldCheck, oldHere }()

	off := store.Settings{Ignored: map[string]bool{"gone": true}}
	manual := off
	manual.FindMods = true
	auto := manual
	auto.AutoAddMods, auto.ModsMaxGB = true, 20
	exp := auto
	exp.SyncDeployedMods = true
	st := SharedFolder{ID: "skyrim-mods", Label: "Skyrim (Vortex mods)", Root: "vortex:skyrimse", Rel: "staging", Kind: mods.KindStaging, SizeGB: 5}
	with := func(f func(*SharedFolder)) SharedFolder { c := st; f(&c); return c }
	dep := with(func(s *SharedFolder) { s.Root, s.Rel, s.Kind = "game:skyrimse", "Data", mods.KindDeployed })

	cases := []struct {
		name   string
		sf     SharedFolder
		s      store.Settings
		synced []string
		here   bool
		want   string
	}{
		{"find mods off", st, off, nil, false, SkipModsOff},
		{"manual", st, manual, nil, false, SkipModsManual},
		{"auto", st, auto, nil, false, ""},
		{"auto, too big", with(func(s *SharedFolder) { s.SizeGB = 30 }), auto, nil, false, SkipModsManual},
		{"auto, no limit", with(func(s *SharedFolder) { s.SizeGB = 300 }), func() store.Settings { a := auto; a.ModsMaxGB = -1; return a }(), nil, false, ""},
		{"profiles, auto", with(func(s *SharedFolder) { s.Rel, s.Kind = "profiles", mods.KindProfiles }), auto, nil, false, ""},
		{"game not managed here", with(func(s *SharedFolder) { s.Root, s.ModGame = "vortex:fallout4", "fallout4" }), auto, nil, false, SkipModGameMissing},
		{"kind doesn't match root", with(func(s *SharedFolder) { s.Kind = mods.KindProfiles }), auto, nil, false, SkipUnsafe},
		{"kind missing on a mod root", with(func(s *SharedFolder) { s.Kind = "" }), auto, nil, false, SkipUnsafe},
		{"mod kind on a save root", with(func(s *SharedFolder) { s.Root = "roaming" }), auto, nil, false, SkipUnsafe},
		{"bad game id", with(func(s *SharedFolder) { s.Root = "vortex:../x" }), auto, nil, false, SkipUnsafe},
		{"unsafe resolved path", with(func(s *SharedFolder) { s.Root = "vortex:bad" }), auto, nil, false, SkipUnsafe},
		{"removed here", with(func(s *SharedFolder) { s.ID = "gone" }), auto, nil, false, SkipRemoved},
		{"overlaps a synced folder", st, auto, []string{`D:\Vortex Mods`}, false, SkipOverlap},
		{"deployed, experimental off", dep, auto, nil, false, SkipModsExperimentalOff},
		{"deployed, never automatic", dep, exp, nil, false, SkipModsManual},
		{"deployed, Vortex deploys here", dep, exp, nil, true, SkipModVortexHere},
	}
	for _, c := range cases {
		here = c.here
		if _, got := Adoptable(c.sf, c.s, func(string) bool { return true }, c.synced); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPublishMods(t *testing.T) {
	old := sourceSince
	sourceSince = func(string) int64 { return 42 }
	defer func() { sourceSince = old }()
	s := store.Settings{Mods: map[string]store.ModFolder{
		"sky-mods": {Kind: mods.KindStaging, Game: "skyrimse", Root: "vortex:skyrimse", Rel: "staging"},
		"sky-data": {Kind: mods.KindDeployed, Game: "skyrimse", Root: "game:skyrimse", Rel: "Data", Role: RoleSource},
		"sky-recv": {Kind: mods.KindDeployed, Game: "skyrimse", Root: "game:skyrimse", Rel: "Data", Role: RoleReceiver},
		"broken":   {Kind: mods.KindStaging, Root: "roaming", Rel: "x"},
	}}
	fs := []syncthing.Folder{
		{ID: FolderID, Path: `C:\meta`},
		{ID: "sky-mods", Label: "Skyrim (Vortex mods)", Path: `D:\Vortex Mods\skyrimse`},
		{ID: "sky-data", Label: "Skyrim (deployed mods)", Path: `D:\Games\Skyrim\Data`},
		{ID: "sky-recv", Label: "Skyrim (deployed mods)", Path: `E:\Games\Skyrim\Data`},
		{ID: "broken", Path: `D:\x`},
	}
	got := publishable(fs, s, "ME", func(string) int64 { return 3<<30 + 1 })
	if len(got) != 2 {
		t.Fatalf("published %d folders, want 2: %+v", len(got), got)
	}
	data, mods0 := got[0], got[1]
	if mods0.ID != "sky-mods" || mods0.Root != "vortex:skyrimse" || mods0.Rel != "staging" || mods0.Kind != mods.KindStaging || mods0.SizeGB != 4 {
		t.Errorf("staging published as %+v", mods0)
	}
	if data.ID != "sky-data" || data.Root != "game:skyrimse" || data.Source != "ME" || data.SourceSince != 42 {
		t.Errorf("deployed published as %+v", data)
	}
}
