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
		upd       *UpdateInfo
		want      string
	}{
		{"all good", on, store.State{LastBackup: &store.BackupRun{OK: true, Finished: now.Add(-time.Hour)}}, nil, nil, ""},
		{"failed backup", on, store.State{LastBackup: failed, LastSuccess: now.Add(-time.Hour * 30)}, nil, nil, "backup-failed"},
		{"failed long ago", on, store.State{LastBackup: &store.BackupRun{Finished: now.Add(-48 * time.Hour)}}, nil, nil, ""},
		{"backups off", store.Settings{}, store.State{LastBackup: failed, LastSuccess: now.AddDate(0, 0, -9)}, nil, nil, ""},
		{"stale", on, store.State{LastBackup: failed, LastSuccess: now.AddDate(0, 0, -4)}, nil, nil, "backup-failed,stale"},
		{"stale from an old OK run", on, store.State{LastBackup: &store.BackupRun{OK: true, Finished: now.AddDate(0, 0, -5)}}, nil, nil, "stale"},
		{"paused", store.Settings{BackupEnabled: true, PausedUntil: time.Now().Add(time.Hour)},
			store.State{LastSuccess: now.AddDate(0, 0, -5)}, nil, nil, ""},
		{"never backed up", on, store.State{}, nil, nil, ""},
		{"conflict and update", on, store.State{}, map[string]int{"a": 1, "b": 0}, &UpdateInfo{Latest: "v9.0.0"}, "conflict,update"},
	} {
		if got := keys(problems(tt.s, tt.st, tt.conflicts, map[string]string{"a": "Game A"}, tt.upd, now)); got != tt.want {
			t.Errorf("%s: problems = %q, want %q", tt.name, got, tt.want)
		}
	}
	ps := problems(on, store.State{LastBackup: failed}, nil, nil, nil, now)
	if len(ps) != 1 || ps[0].body != "Drive not found (and 1 more)" {
		t.Errorf("failure text: %+v", ps)
	}
}

func TestTomorrowMorning(t *testing.T) {
	got := tomorrowMorning(time.Date(2026, 12, 31, 23, 30, 0, 0, time.Local))
	if want := time.Date(2027, 1, 1, 6, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("tomorrowMorning = %v, want %v", got, want)
	}
}
