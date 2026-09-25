package main

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/store"
)

func TestProblems(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local)
	on := store.Settings{BackupEnabled: true}
	keys := func(ps []problem) string {
		var ks []string
		for _, p := range ps {
			ks = append(ks, strings.SplitN(p.key, ":", 2)[0])
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	failed := &store.BackupRun{Finished: now.Add(-time.Hour), Errors: []string{"Drive not found", "x"}}
	for _, tt := range []struct {
		name      string
		s         store.Settings
		st        store.State
		conflicts map[string]int
		newer     map[string]newerSave
		upd       *UpdateInfo
		want      string
	}{
		{"all good", on, store.State{LastBackup: &store.BackupRun{OK: true, Finished: now.Add(-time.Hour)}}, nil, nil, nil, ""},
		{"failed backup", on, store.State{LastBackup: failed, LastSuccess: now.Add(-time.Hour * 30)}, nil, nil, nil, "backup-failed"},
		{"failed long ago", on, store.State{LastBackup: &store.BackupRun{Finished: now.Add(-48 * time.Hour)}}, nil, nil, nil, ""},
		{"backups off", store.Settings{}, store.State{LastBackup: failed, LastSuccess: now.AddDate(0, 0, -9)}, nil, nil, nil, ""},
		{"stale", on, store.State{LastBackup: failed, LastSuccess: now.AddDate(0, 0, -4)}, nil, nil, nil, "backup-failed,stale"},
		{"stale from an old OK run", on, store.State{LastBackup: &store.BackupRun{OK: true, Finished: now.AddDate(0, 0, -5)}}, nil, nil, nil, "stale"},
		{"paused", store.Settings{BackupEnabled: true, PausedUntil: time.Now().Add(time.Hour)},
			store.State{LastSuccess: now.AddDate(0, 0, -5)}, nil, map[string]newerSave{"a": {Host: "Desk", At: now.Add(-time.Hour)}}, nil, ""},
		{"never backed up", on, store.State{}, nil, nil, nil, ""},
		{"conflict and update", on, store.State{}, map[string]int{"a": 1, "b": 0}, nil, &UpdateInfo{Latest: "v9.0.0"}, "conflict,update"},
		{"newer save elsewhere", on, store.State{}, nil, map[string]newerSave{"a": {Host: "Desk", At: now.Add(-time.Hour)}}, nil, "newer"},
		{"newer, sync off", store.Settings{SyncDisabled: true}, store.State{}, nil, map[string]newerSave{"a": {Host: "Desk", At: now.Add(-time.Hour)}}, nil, ""},
	} {
		if got := keys(problems(tt.s, tt.st, tt.conflicts, map[string]string{"a": "Game A"}, tt.newer, tt.upd, now)); got != tt.want {
			t.Errorf("%s: problems = %q, want %q", tt.name, got, tt.want)
		}
	}
	ps := problems(on, store.State{LastBackup: failed}, nil, nil, nil, nil, now)
	if len(ps) != 1 || ps[0].body != "Drive not found (and 1 more)" {
		t.Errorf("failure text: %+v", ps)
	}
	ps = problems(on, store.State{}, nil, map[string]string{"a": "Game A"}, map[string]newerSave{"a": {Host: "Desk", At: now}}, nil, now)
	if len(ps) != 1 || ps[0].title != "Game A has a newer save on Desk" || !strings.HasPrefix(ps[0].key, "newer:a:") {
		t.Errorf("newer save: %+v", ps)
	}
}

func TestTomorrowMorning(t *testing.T) {
	got := tomorrowMorning(time.Date(2026, 12, 31, 23, 30, 0, 0, time.Local))
	if want := time.Date(2027, 1, 1, 6, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("tomorrowMorning = %v, want %v", got, want)
	}
}
