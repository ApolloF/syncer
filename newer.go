package main

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/store"
)

// A PC that is off (or has Syncer's sync paused) can't tell the others about
// a save it made. If it backed that save up to the same Google account, its
// info file next to the backup does: this PC warns before you play an older
// save here.

// newerSave is a newer save of a synced game that another PC backed up.
// CanGet: it can be taken from the backup (see cloudpull.go); Why says why
// that didn't happen by itself.
type newerSave struct {
	Host   string
	At     time.Time
	CanGet bool
	Why    string
}

const (
	newerEvery = 10 * time.Minute
	newerSlack = 2 * time.Minute     // clocks and file systems differ a little
	newerMax   = 60 * 24 * time.Hour // info older than this is ignored
)

func (a *App) newerLoop(ctx context.Context) {
	sleep(ctx, 3*time.Minute) // let startup settle
	for ctx.Err() == nil {
		a.checkNewer(ctx)
		sleep(ctx, newerEvery)
	}
}

// checkNewer looks for newer saves of every synced game on other PCs, and
// takes the ones it safely can from their backups.
func (a *App) checkNewer(ctx context.Context) {
	found, took := pullNewer(ctx, a.gameRunning)
	if ctx.Err() != nil {
		return
	}
	if len(took) > 0 {
		a.forgetConflicts()
		runtime.EventsEmit(a.ctx, "toast", "Took newer saves from another PC's backup: "+strings.Join(took, ", "))
	}
	a.mu.Lock()
	changed := !maps.Equal(a.newer, found)
	a.newer = found
	a.mu.Unlock()
	if changed {
		runtime.EventsEmit(a.ctx, "changed")
	}
}

// newerElsewhere reports the newest save of f another PC backed up, if it's
// newer than every save here, and that PC's info about it. A newest save
// that is a version decided against doesn't count: the user already chose
// between the two, and the one kept can well be the older.
func newerElsewhere(target string, f backup.Folder, exclude []string, now time.Time) (newerSave, backup.Info, bool) {
	var best backup.Info
	rejected := rejectedVersions(f.ID)
	isRejected := func(t time.Time) bool {
		for _, r := range rejected {
			if d := t.Unix() - r.Mod; d >= -2 && d <= 2 {
				return true
			}
		}
		return false
	}
	for _, in := range backup.ReadInfos(target, f.ID) {
		if !in.Mine() && now.Sub(in.BackedUp) < newerMax && in.Newest.After(best.Newest) && !isRejected(in.Newest) {
			best = in
		}
	}
	if best.Newest.IsZero() {
		return newerSave{}, best, false
	}
	if local := backup.Newest(f.Path, exclude); !best.Newest.After(local.Add(newerSlack)) {
		return newerSave{}, best, false
	}
	return newerSave{Host: cmpOr(best.Host, "another PC"), At: best.Newest}, best, true
}

// recheckNewer drops the newer saves that have reached this PC since the last
// check. Only the flagged games are looked at, and only on this PC, so it's
// cheap enough to run whenever Syncthing brings in changes.
func (a *App) recheckNewer() {
	newer := a.newerNow()
	if len(newer) == 0 {
		return
	}
	fs, err := syncedFolders()
	if err != nil {
		return
	}
	s := store.LoadSettings()
	left := map[string]newerSave{}
	for _, f := range fs {
		n, ok := newer[f.ID]
		if ok && n.At.After(backup.Newest(f.Path, s.Exclude[dismissKey(f.Path)]).Add(newerSlack)) {
			left[f.ID] = n
		}
	}
	a.mu.Lock()
	changed := len(left) != len(a.newer)
	if maps.Equal(a.newer, newer) { // no full check ran meanwhile
		a.newer = left
	}
	a.mu.Unlock()
	if changed {
		runtime.EventsEmit(a.ctx, "changed")
	}
}

// newerNow returns the newer saves found by the last check.
func (a *App) newerNow() map[string]newerSave {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.newer
}
