package main

import (
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/store"
)

// backupDetail is what a game's row says about its backup.
type backupDetail struct {
	at     time.Time
	bytes  int64
	points int
}

// addDetails fills in each game's backup details, exclusions and newer-save
// warning. Sizes come from the local backup index and restore points from a
// directory listing in Drive, cached for a minute since the list refreshes
// often.
func (a *App) addDetails(fs []FolderView, s store.Settings) {
	st := store.LoadState()
	target, ok := backupTarget(s)
	a.mu.Lock()
	cache, newer := a.details, a.newer
	a.mu.Unlock()
	fresh := map[string]backupDetail{}
	for i := range fs {
		f := &fs[i]
		f.BackedUp = st.FolderBackups[f.ID]
		f.Exclude = s.Exclude[dismissKey(f.Path)]
		if n, found := newer[f.ID]; found && f.Sync {
			f.NewerOn, f.NewerAt = n.Host, n.At
		}
		d, cached := cache[f.ID]
		if !cached || time.Since(d.at) > time.Minute {
			d = backupDetail{at: time.Now()}
			d.bytes, _ = backup.IndexSize(f.ID)
			if ok {
				d.points = len(backup.Points(target, f.ID))
			}
		}
		fresh[f.ID] = d
		f.BackupBytes, f.Points = d.bytes, d.points
	}
	a.mu.Lock()
	a.details = fresh
	a.mu.Unlock()
}

// forgetDetails drops cached backup details after a backup or restore.
func (a *App) forgetDetails() {
	a.mu.Lock()
	a.details = nil
	a.mu.Unlock()
}
