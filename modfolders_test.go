package main

import (
	"slices"
	"testing"

	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
)

func TestWantAutoMod(t *testing.T) {
	s := store.Settings{FindMods: true, AutoAddMods: true, ModsMaxGB: 20, Dismissed: map[string]bool{}}
	st := mods.Found{Kind: mods.KindStaging, Path: `D:\Vortex Mods\skyrimse`}
	if !wantAutoMod(st, 5<<30, 10, s) {
		t.Error("staging folder within the limit not wanted")
	}
	if wantAutoMod(st, 21<<30, 10, s) {
		t.Error("staging folder over the limit wanted")
	}
	if wantAutoMod(st, 0, 0, s) {
		t.Error("empty staging folder wanted")
	}
	if wantAutoMod(mods.Found{Kind: mods.KindDeployed, Path: `D:\Games\Skyrim\Data`}, 1, 1, s) {
		t.Error("deployed mods wanted automatically")
	}
	s.Dismissed[dismissKey(st.Path)] = true
	if wantAutoMod(st, 1, 1, s) {
		t.Error("dismissed staging folder wanted")
	}
	s.ModsMaxGB = -1
	delete(s.Dismissed, dismissKey(st.Path))
	if !wantAutoMod(st, 500<<30, 10, s) {
		t.Error("no limit, still skipped")
	}
}

func TestToBackupOnlyMovesMod(t *testing.T) {
	s := store.Settings{BackupOnly: map[string]store.LocalFolder{}, Ignored: map[string]bool{}, Dismissed: map[string]bool{},
		NoBackup: map[string]bool{"sky-mods": true},
		Mods:     map[string]store.ModFolder{"sky-mods": {Kind: mods.KindStaging, Root: "vortex:skyrimse", Rel: "staging"}}}
	bid := toBackupOnly(&s, "sky-mods", "Skyrim (Vortex mods)", `D:\Vortex Mods\skyrimse`)
	if _, ok := s.Mods["sky-mods"]; ok {
		t.Error("mod entry left under the synced id")
	}
	if s.Mods[bid].Root != "vortex:skyrimse" {
		t.Errorf("mod entry not moved to %s: %+v", bid, s.Mods)
	}
	if !s.NoBackup[bid] {
		t.Error("backup choice not kept: a mod folder not backed up must stay off")
	}
}

func TestHoldable(t *testing.T) {
	s := store.Settings{Mods: map[string]store.ModFolder{
		"st":   {Kind: mods.KindStaging},
		"src":  {Kind: mods.KindDeployed, Role: meta.RoleSource},
		"recv": {Kind: mods.KindDeployed, Role: meta.RoleReceiver},
	}}
	for id, want := range map[string]bool{"st": true, "src": true, "recv": false, "save": false} {
		if got := holdable(s, id); got != want {
			t.Errorf("holdable(%s) = %v, want %v", id, got, want)
		}
	}
}

func TestModIgnores(t *testing.T) {
	got := withSyncIgnores(mods.KindStaging, []string{"*.log"})
	for _, want := range []string{"steam_autocloud.vdf", "/" + mods.StagingMarker, "vortex.deployment*.json", "*.log"} {
		if !slices.Contains(got, want) {
			t.Errorf("staging ignores %v lack %q", got, want)
		}
	}
	if slices.Contains(withSyncIgnores("", nil), "/"+mods.StagingMarker) {
		t.Error("a save folder got the staging lines")
	}
}
