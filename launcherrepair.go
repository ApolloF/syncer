package main

// Repairing a launcher's data folder that Syncthing stopped because its
// marker (.stfolder) or the folder itself went missing: something outside
// Syncer emptied or replaced it, and the launcher then started over with
// fresh files.
//
// Putting the marker back would make Syncthing rescan, find the files it
// knew missing and send their deletion to every other PC. Instead the folder
// is taken out of Syncthing (which forgets its index) and added back under
// the same id. Syncthing then treats it like a PC joining the folder: it
// creates the marker, takes what the other PCs have and sends nothing as
// deleted. What the folder holds by then is offered as this PC's own
// changes, and where both sides changed a file Syncthing keeps both (the
// older as a conflict copy).
//
// Before it is added back, the files the backup has are brought back. This
// relies on the launcher data contract (see apiLauncherData): the launcher
// keeps one file per PC and account and adds up every file in the folder
// (playtime summed, achievements joined, the newest settings win), and it
// skips Syncthing's conflict copies. So a file that's missing is restored as
// it was, and a file the launcher has already started again gets the
// backup's copy next to it (<name>.restored-<time><ext>): the launcher counts
// the old playtime alongside the new, and nothing counts twice.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/fsx"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// repairAgain is how long after a repair the same folder isn't repaired
// again: when adding it back didn't help, repeating it every call wouldn't.
const repairAgain = 6 * time.Hour

// needsRepair reports whether a launcher's data folder lost what Syncthing
// needs to sync it safely: its marker, or the whole folder while the
// launcher's own folder is still there (a launcher that is gone stays gone).
func needsRepair(f syncthing.Folder, st syncthing.FolderStatus) bool {
	switch {
	case markerMissing(st.Error):
		return true
	case pathMissing(st.Error):
		fi, err := os.Stat(filepath.Dir(f.Path))
		return err == nil && fi.IsDir()
	}
	return false
}

// repairResult is what a repair brought back.
type repairResult struct {
	Restored int       // files restored as they were
	Beside   int       // backup copies put next to a file the launcher started again
	Newest   time.Time // the newest of those copies
	NoBackup string    // why nothing could be restored, if it couldn't
	Done     bool      // repaired (false: another Syncer got there first)
}

// repairLaunchers repairs every launcher data folder that needs it (see
// needsRepair) and adds back those a repair took out of Syncthing earlier.
// It reports whether it changed anything.
func repairLaunchers(ctx context.Context, c *syncthing.Client) bool {
	changed := finishRejoins(ctx, c)
	s := store.LoadSettings()
	if len(s.Launchers) == 0 || s.SyncDisabled || s.Paused() {
		return changed
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return changed
	}
	for _, f := range fs {
		launcher, ok := s.Launchers[f.ID]
		if !ok || f.ID == meta.FolderID {
			continue
		}
		st, err := c.FolderStatus(ctx, f.ID)
		if err != nil || !needsRepair(f, st) {
			continue
		}
		r, err := repairLauncherData(ctx, c, f, launcher)
		if err != nil {
			logx.Printf("couldn't repair %s's data folder: %v", launcher, err)
			continue
		}
		changed = changed || r.Done
	}
	return changed
}

// repairLauncherData restores what the backup has of launcher's data folder
// f and adds it back to Syncthing with a fresh index (see the top of this
// file). The caller has checked that it needs it.
func repairLauncherData(ctx context.Context, c *syncthing.Client, f syncthing.Folder, launcher string) (repairResult, error) {
	var r repairResult
	if t, ok := store.LoadState().Repaired[f.ID]; ok && time.Since(t) < repairAgain {
		return r, fmt.Errorf("it was repaired at %s already; Syncer tries again later", t.Format("15:04"))
	}
	unlock, err := waitBackupLock(ctx, 30*time.Second)
	if err != nil {
		return r, errors.New("a backup is running; Syncer tries again after it")
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	// Again, now that no other Syncer can be at it: one may have repaired
	// it meanwhile.
	f, err = findFolder(ctx, c, f.ID)
	if err != nil {
		return r, nil
	}
	if st, err := c.FolderStatus(ctx, f.ID); err != nil || !needsRepair(f, st) {
		return r, err
	}
	store.UpdateState(func(st *store.State) {
		if st.Repaired == nil {
			st.Repaired = map[string]time.Time{}
		}
		st.Repaired[f.ID] = time.Now()
	})

	// Restored first, so Syncthing's first scan already sees the files.
	r, err = restoreLauncherData(f)
	if err != nil {
		r.NoBackup = err.Error()
	}
	lf := store.LocalFolder{ID: f.ID, Label: cmpOr(f.Label, f.ID), Path: f.Path}
	store.UpdateState(func(st *store.State) {
		if st.Rejoin == nil {
			st.Rejoin = map[string]store.LocalFolder{}
		}
		st.Rejoin[f.ID] = lf
	})
	if err := c.RemoveFolder(ctx, f.ID); err != nil {
		store.UpdateState(func(st *store.State) { delete(st.Rejoin, f.ID) })
		return r, fmt.Errorf("couldn't take it out of Syncthing: %w", err)
	}
	// Adding it back keeps a restore point of the files here first, which
	// takes the backup lock itself.
	unlock()
	locked = false

	switch {
	case r.NoBackup != "":
		logx.Printf("repairing %s's data folder: nothing could be restored (%s)", launcher, r.NoBackup)
	case r.Restored+r.Beside == 0:
		logx.Printf("repairing %s's data folder: the backup had nothing that's missing here", launcher)
	}
	if err := rejoin(ctx, c, lf); err != nil {
		return r, fmt.Errorf("couldn't add it back to Syncthing, Syncer tries again later: %w", err)
	}
	logx.Printf("repaired %s's data folder: %s", launcher, r.describe())
	r.Done = true
	return r, nil
}

// describe says in a few words what a repair brought back.
func (r repairResult) describe() string {
	n := r.Restored + r.Beside
	if n == 0 {
		return "it syncs again with a fresh start"
	}
	s := fmt.Sprintf("restored %d file(s) from the backup, the newest from %s", n, r.Newest.Format("2006-01-02 15:04"))
	if r.Beside > 0 {
		s += fmt.Sprintf(" (%d next to the launcher's new file)", r.Beside)
	}
	return s
}

// rejoin adds a launcher data folder a repair took out of Syncthing back to
// it, under the same id and label, shared with every paired PC, with the
// settings Syncer gives every synced folder.
func rejoin(ctx context.Context, c *syncthing.Client, lf store.LocalFolder) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	ds, _ := c.Devices(ctx)
	var others []string
	for _, d := range ds {
		if d.DeviceID != st.MyID {
			others = append(others, d.DeviceID)
		}
	}
	if err := meta.AddFolderSpec(ctx, c, meta.FolderSpec{ID: lf.ID, Label: lf.Label, Path: lf.Path}, st.MyID, others); err != nil {
		return err
	}
	store.UpdateState(func(st *store.State) { delete(st.Rejoin, lf.ID) })
	_, _ = meta.Reconcile(ctx, c)
	_ = syncPause(ctx, c) // repaired mid-pause: it waits too
	meta.RefreshCache(ctx, c)
	return nil
}

// finishRejoins adds back the launcher data folders a repair took out of
// Syncthing but couldn't add back. It reports whether it added any.
func finishRejoins(ctx context.Context, c *syncthing.Client) bool {
	pending := store.LoadState().Rejoin
	if len(pending) == 0 {
		return false
	}
	fs, err := c.Folders(ctx)
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, f := range fs {
		have[f.ID] = true
	}
	added := false
	for id, lf := range pending {
		if have[id] { // added back meanwhile (by another PC's list, say)
			store.UpdateState(func(st *store.State) { delete(st.Rejoin, id) })
			continue
		}
		if err := rejoin(ctx, c, lf); err != nil {
			logx.Printf("couldn't add %s back to Syncthing: %v", lf.Label, err)
			continue
		}
		logx.Printf("added %s back to Syncthing", lf.Label)
		added = true
	}
	return added
}

// waitBackupLock takes the backup lock, waiting up to d for a running backup.
func waitBackupLock(ctx context.Context, d time.Duration) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		if unlock, err := backup.Lock(); err == nil {
			return unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, backup.ErrBusy
		case <-time.After(2 * time.Second):
		}
	}
}

// restoreLauncherData brings back what f's backup has that f is missing
// (see planRestore). The caller holds the backup lock.
func restoreLauncherData(f syncthing.Folder) (repairResult, error) {
	var r repairResult
	s := store.LoadSettings()
	target, ok := backupTarget(s)
	if !ok {
		return r, errors.New("the backup isn't reachable")
	}
	copies := backup.Copies(target, f.ID)
	if len(copies) == 0 {
		return r, errors.New("there's no backup of it")
	}
	if err := os.MkdirAll(f.Path, 0o755); err != nil {
		return r, err
	}
	m := backup.LoadMatcher(f.Path, s.Exclude[dismissKey(f.Path)]...)
	steps := planRestore(f.Path, copies, m.Ignored, fileCreated)
	for _, st := range steps {
		if err := putCopy(st.From.Path, filepath.Join(f.Path, st.To), st.From.Modified); err != nil {
			logx.Printf("couldn't restore %s: %v", st.To, err)
			continue
		}
		if st.Beside {
			r.Beside++
		} else {
			r.Restored++
		}
		if st.From.Modified.After(r.Newest) {
			r.Newest = st.From.Modified
		}
	}
	return r, nil
}

// restoreStep is one file a repair brings back from the backup.
type restoreStep struct {
	To     string      // where it goes, relative to the folder
	From   backup.Copy // the backup's copy
	Beside bool        // next to a file the launcher started again
}

// planRestore decides what to bring back into dir from its backup's copies
// (by lowercased rel, newest first; see backup.Copies). skip tells the files
// that aren't the launcher's (Syncthing's markers, temporary files, the
// folder's ignore patterns); born tells when a file in dir was created.
//
//   - A file that's missing is restored as it was: its newest copy.
//   - A file that's there was written again after the folder was emptied
//     (the launcher started over), so any copy of it modified since the file
//     was created is the launcher's new data, already counted. Its newest
//     copy from before is put next to it, unless that's what it holds
//     anyway or it was put there already. A file whose creation time can't
//     be told, or that has no older copy, is left alone.
func planRestore(dir string, copies map[string][]backup.Copy, skip func(rel string) bool, born func(path string) (time.Time, bool)) []restoreStep {
	keys := make([]string, 0, len(copies))
	for k := range copies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var steps []restoreStep
	for _, k := range keys {
		cs := copies[k]
		if len(cs) == 0 || skip(cs[0].Rel) {
			continue
		}
		rel := cs[0].Rel
		local := filepath.Join(dir, rel)
		fi, err := os.Lstat(local)
		if errors.Is(err, fs.ErrNotExist) {
			steps = append(steps, restoreStep{To: rel, From: cs[0]})
			continue
		}
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		created, ok := born(local)
		if !ok {
			continue
		}
		i := 0
		for i < len(cs) && !cs[i].Modified.Before(created) {
			i++
		}
		if i == len(cs) {
			continue
		}
		from := cs[i]
		if same, err := fsx.SameContent(local, from.Path); err != nil || same {
			continue
		}
		beside := restoredName(rel, from.Modified)
		if _, err := os.Lstat(filepath.Join(dir, beside)); err == nil {
			continue // restored by an earlier repair
		}
		steps = append(steps, restoreStep{To: beside, From: from, Beside: true})
	}
	return steps
}

// restoredName names the backup's copy of rel, modified at t, put next to
// it: PC-1.json → PC-1.restored-20261002-155700.json.
func restoredName(rel string, t time.Time) string {
	ext := filepath.Ext(rel)
	return strings.TrimSuffix(rel, ext) + ".restored-" + t.Format("20060102-150405") + ext
}

// fileCreated is when the file at p was created. NTFS keeps a file's
// creation time when a program saves it by writing a new file and renaming
// it over the old one, so it is when the launcher first wrote the file.
var fileCreated = func(p string) (time.Time, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, d.CreationTime.Nanoseconds()), true
}

// putCopy copies src to dst with modification time mtime. It never replaces
// a file: one that appeared at dst meanwhile is left as it is.
func putCopy(src, dst string, mtime time.Time) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".syncer-tmp"
	if err := copyTo(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	defer os.Remove(tmp)
	_ = os.Chtimes(tmp, mtime, mtime)
	// A hard link, unlike a rename, fails when dst exists.
	return os.Link(tmp, dst)
}

func copyTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
