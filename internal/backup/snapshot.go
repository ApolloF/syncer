package backup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Snapshot saves the files currently in f.Path as a restore point, so the
// saves a PC had before it started syncing a folder can always be brought
// back, whichever copy the sync ends up picking. Files the backup mirror
// already holds unchanged are skipped: restoring to this point recovers them
// from the mirror or from the versions taken when they are later replaced.
// It returns how many files were saved.
func Snapshot(ctx context.Context, target string, f Folder) (int, error) {
	m := LoadMatcher(f.Path, f.Exclude...)
	mirror := filepath.Join(target, f.ID)
	var rels []string
	err := filepath.WalkDir(f.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == f.Path {
				return err
			}
			return nil
		}
		rel, _ := filepath.Rel(f.Path, p)
		if rel == "." {
			return nil
		}
		if m.Ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || strings.HasSuffix(p, tmpSuffix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if bk, err := os.Stat(filepath.Join(mirror, rel)); err == nil && bk.Size() == info.Size() && sameTime(bk.ModTime(), info.ModTime()) {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil // nothing here yet: nothing to protect
	}
	if err != nil {
		return 0, err
	}
	if len(rels) == 0 {
		return 0, nil
	}

	// Don't share a stamp directory with a backup run in progress.
	unlock, err := waitLock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	t := time.Now()
	dir := filepath.Join(target, VersionsDir, f.ID, t.Format(stampFmt))
	for isDir(dir) {
		t = t.Add(time.Second)
		dir = filepath.Join(target, VersionsDir, f.ID, t.Format(stampFmt))
	}
	for _, rel := range rels {
		src := filepath.Join(f.Path, rel)
		fi, err := os.Stat(src)
		if err != nil {
			continue // gone meanwhile
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		if err := copyFile(src, dst); err != nil {
			return 0, err
		}
		_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	keepPoint(dir)
	return len(rels), nil
}

// A restore point holding a PC's own saves from before it started syncing
// (or from before a restore replaced them) is kept for a year instead of the
// usual history days: it may be the only copy left of those saves. The mark
// is a file next to the point's folder, so restores never see it.
const (
	keepSuffix = ".keep"
	keepFor    = 365 * 24 * time.Hour
)

func keepPoint(dir string) { _ = os.WriteFile(dir+keepSuffix, nil, 0o644) }

// kept reports whether the restore point at dir is marked and less than
// keepFor old.
func kept(dir string, point time.Time) bool {
	_, err := os.Stat(dir + keepSuffix)
	return err == nil && time.Since(point) < keepFor
}

// Keep moves a file into the folder's history as a new restore point (it can
// be brought back with "As it was before <now>").
// It's a save the user chose against, so it is kept like a pre-sync snapshot.
func Keep(target, id, src, rel string) error {
	dir := filepath.Join(target, VersionsDir, id, time.Now().Format(stampFmt))
	if err := moveTo(src, filepath.Join(dir, rel)); err != nil {
		return err
	}
	keepPoint(dir)
	return nil
}

// waitLock takes the backup lock, waiting for a running backup to finish.
func waitLock(ctx context.Context) (func(), error) {
	for {
		if u, err := lock(); err == nil {
			return u, nil
		}
		select {
		case <-ctx.Done():
			return nil, ErrBusy
		case <-time.After(2 * time.Second):
		}
	}
}
