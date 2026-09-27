package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/store"
)

// Settling two versions of a save is remembered: when (so the version put
// aside is never taken back from another PC's backup by itself, see
// newerElsewhere and the backup's mirror), and what went where (so the
// choice can be changed later).

// maxDecisions caps the remembered choices.
const maxDecisions = 100

func init() { backup.SettledAt = settledAt }

// decided remembers a choice between two versions of a save, and tells the
// other PCs: through this PC's backup info right away (Google Drive) and its
// metadata (Syncthing).
func decided(d store.Decision) {
	d.At = time.Now()
	store.UpdateState(func(st *store.State) {
		if st.Settled == nil {
			st.Settled = map[string]time.Time{}
		}
		st.Settled[d.Folder] = d.At
		st.Decisions = slices.DeleteFunc(st.Decisions, func(x store.Decision) bool {
			return x.Folder == d.Folder && strings.EqualFold(x.Rel, d.Rel)
		})
		st.Decisions = append(st.Decisions, d)
		if n := len(st.Decisions); n > maxDecisions {
			st.Decisions = st.Decisions[n-maxDecisions:]
		}
	})
	if t, ok := backupTarget(store.LoadSettings()); ok {
		backup.NoteSettled(t, d.Folder)
	}
}

// settledAt is when two versions of a folder's saves were last settled, on
// any PC: here, as another PC published it, or as its backup info says.
// Times in the future (a wrong clock, or a bad file) don't count.
func settledAt(id string) time.Time {
	best := store.LoadState().Settled[id]
	limit := time.Now().Add(time.Hour)
	consider := func(t time.Time) {
		if t.After(best) && t.Before(limit) {
			best = t
		}
	}
	consider(meta.PeerSettled("", id))
	if t, ok := backupTarget(store.LoadSettings()); ok {
		for _, in := range backup.ReadInfos(t, id) {
			consider(in.Settled)
		}
	}
	return best
}

// DecisionView is an earlier choice between two versions of a save.
type DecisionView struct {
	At    int64  `json:"at"` // unix seconds
	Rel   string `json:"rel"`
	Kept  string `json:"kept"`  // PC the version kept came from ("" = unknown)
	Other string `json:"other"` // PC the version put aside came from
}

// Decisions lists a folder's earlier choices between two versions whose
// other version can still be brought back, newest first.
func (a *App) Decisions(id string) []DecisionView {
	out := []DecisionView{}
	ds := store.LoadState().Decisions
	for i := len(ds) - 1; i >= 0; i-- {
		d := ds[i]
		if d.Folder != id || d.Put == "" {
			continue
		}
		if fi, err := os.Stat(d.Put); err != nil || !fi.Mode().IsRegular() {
			continue // thinned out of the history since
		}
		out = append(out, DecisionView{At: d.At.Unix(), Rel: d.Rel, Kept: d.Kept, Other: d.Other})
	}
	return out
}

// SwitchDecision changes an earlier choice: the version put aside comes
// back, and the one used until now is put aside instead (into history, as
// before, never deleted). The change syncs to the other PCs.
func (a *App) SwitchDecision(id, rel string) error {
	if a.gameRunning() {
		return errors.New("close the game first")
	}
	if applying.has(id) {
		return errApplying
	}
	f, err := folderByID(id)
	if err != nil {
		return err
	}
	var d store.Decision
	found := false
	for _, x := range store.LoadState().Decisions {
		if x.Folder == id && strings.EqualFold(x.Rel, rel) {
			d, found = x, true
		}
	}
	if !found || !safeRel(d.Rel) {
		return errors.New("that choice isn't remembered anymore")
	}
	from := d.Put
	fi, err := os.Stat(from)
	if err != nil || !fi.Mode().IsRegular() {
		return errors.New("the other version is no longer in the history; pick a restore point instead")
	}
	to := filepath.Join(f.Path, d.Rel)

	// The version used until now goes into history first (a copy: if the
	// swap fails, it's still in place).
	put := ""
	if cur, err := os.Stat(to); err == nil && cur.Mode().IsRegular() {
		keep, _ := keeper(f)
		if put, err = keep(to, d.Rel, false); err != nil {
			return fmt.Errorf("could not keep the current version first: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	tmp := to + ".syncer-tmp"
	if err := conflict.CopyFile(from, tmp); err != nil { // keeps its time
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, to); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not swap the files (is the game running?): %w", err)
	}
	logx.Printf("conflict in %s: switched %s to the version from %s", f.Label, d.Rel, cmpOr(d.Other, "the other PC"))
	decided(store.Decision{Folder: id, Rel: d.Rel, Kept: d.Other, Other: d.Kept, Put: put})
	a.forgetConflicts()
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// safeRel reports whether rel stays inside the folder it's relative to.
func safeRel(rel string) bool {
	rel = filepath.Clean(rel)
	return rel != "." && !filepath.IsAbs(rel) && filepath.VolumeName(rel) == "" &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
