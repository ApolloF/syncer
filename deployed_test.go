package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
)

func TestScopeLines(t *testing.T) {
	if got := scopeLines(nil, nil, nil); !slices.Equal(got, []string{"*"}) {
		t.Errorf("nothing known: %v, want everything ignored", got)
	}
	applied := &mods.Inventory{Files: []mods.InvFile{{Rel: "a.esp"}, {Rel: "meshes/b.nif"}}}
	target := &mods.Inventory{Files: []mods.InvFile{{Rel: "A.esp"}, {Rel: "c.esp"}, {Rel: "bad[1].esp"}}}
	got := scopeLines(nil, applied, target)
	if want := []string{"!/a.esp", "!/c.esp", "!/meshes/b.nif", "*"}; !slices.Equal(got, want) {
		t.Errorf("receiver mid-update: %v, want %v", got, want)
	}
	ss := &sourceState{Inv: mods.Inventory{Files: []mods.InvFile{{Rel: "new.esp"}}},
		Tombstones: map[string]time.Time{"old.esp": time.Now(), "ancient.esp": time.Now().Add(-2 * tombstoneAge)}}
	got = scopeLines(ss)
	if want := []string{"!/new.esp", "!/old.esp", "*"}; !slices.Equal(got, want) {
		t.Errorf("source: %v, want %v", got, want)
	}
	// The user's own exclusions come first, so they win (first match).
	lines := append(withSyncIgnores(mods.KindDeployed, []string{"*.log"}), got...)
	if i, j := slices.Index(lines, "*.log"), slices.Index(lines, "!/new.esp"); i < 0 || i > j || lines[len(lines)-1] != "*" {
		t.Errorf("order: %v", lines)
	}
}

func TestGameRunning(t *testing.T) {
	procs := []string{`C:\Windows\explorer.exe`, `D:\Games\Skyrim\SkyrimSE.exe`, "svchost.exe"}
	if !gameRunning(`D:\Games\Skyrim`, procs) {
		t.Error("running game not seen")
	}
	if gameRunning(`D:\Games\Skyrim2`, procs) || gameRunning("", procs) {
		t.Error("game seen running that isn't")
	}
}

func TestModHeldProblem(t *testing.T) {
	s := store.Settings{Mods: map[string]store.ModFolder{"sky": {GameName: "Skyrim Special Edition"}}}
	st := store.State{ModSync: map[string]store.ModSyncState{
		"sky":   {Phase: phaseHeld, Held: "the last update failed its checks"},
		"other": {Phase: phasePending},
		"blank": {Phase: phaseHeld},
	}}
	ps := problems(s, st, nil, nil, nil, nil, time.Now())
	var got []string
	for _, p := range ps {
		got = append(got, p.title+": "+p.body)
	}
	if len(ps) != 2 || !strings.Contains(strings.Join(got, "|"), "Mod updates for Skyrim Special Edition are on hold: The last update failed its checks.") {
		t.Errorf("problems = %v", got)
	}
}
