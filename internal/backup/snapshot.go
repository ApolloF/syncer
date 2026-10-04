package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Snapshot saves the files currently in f.Path as a restore point, so the
// saves a PC had before it started syncing a folder can always be brought
// back, whichever copy the sync ends up picking. Files the backup mirror
// already holds unchanged are skipped: restoring to this point recovers them
// from the mirror or from the versions taken when they are later replaced.
// It returns how many files were saved.
func Snapshot(ctx context.Context, target string, f Folder) (int, error) {
	m := LoadMatcher(f.Path, f.Exclude...)
	sc := f.scope()
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
		if m.Ignored(rel) || sc.skip(rel, d.IsDir()) {
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
	dir, t := newAsidePoint(target, f.ID)
	org := newOrigins(f.ID, f.Solo)
	defer org.save(target, t.Format(stampFmt))
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
		org.add(org.lookup(rel))
	}
	return len(rels), nil
}

// Keep puts a file into the folder's history as a new restore point (it can
// be brought back with "As it was before <now>"). With move the file goes
// there, otherwise a copy does. It waits up to a minute for a running backup,
// which would otherwise be thinning the same history, and returns ErrBusy
// after that. It returns where the file was put.
func Keep(target, id, src, rel string, move bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keepWait)
	defer cancel()
	unlock, err := waitLock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	return KeepLocked(target, id, src, rel, move)
}

// KeepLocked is Keep for a caller that already holds the backup lock.
func KeepLocked(target, id, src, rel string, move bool) (string, error) {
	// A restore point of its own: two files kept in the same second (or a
	// snapshot taken just before) must never replace each other.
	dir, t := newAsidePoint(target, id)
	defer writeOrigin(target, id, t.Format(stampFmt), Origin{By: []string{hostName}})
	dst := filepath.Join(dir, rel)
	if move {
		return dst, moveTo(src, dst)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return dst, os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// AsideDir, next to a folder's restore points, marks the points saved
// outside a backup run: the saves a PC had before it started syncing, the
// files a restore replaced, the losing copy of a conflict. A point a backup
// run makes holds what each file was before that run replaced it, so
// restoring to an older time lays those points over each other; an aside
// point holds a file as it was at some moment instead, possibly one nobody
// chose to keep, so it only counts when it is the point restored to. A mark
// is an empty file <target>\.versions\<id>\.aside\<stamp>.
const AsideDir = ".aside"

// KeepAside makes a new, empty restore point for files saved outside a
// backup run (see AsideDir), pinned, and returns its folder. The caller holds
// the backup lock and puts the files in.
func KeepAside(target, id string) (string, error) {
	if !paths.ValidID(id) {
		return "", fmt.Errorf("unsupported folder id %q", id)
	}
	dir, _ := newAsidePoint(target, id)
	return dir, os.MkdirAll(dir, 0o755)
}

// newAsidePoint picks a stamp no point of id has yet, from now on, and
// marks its point pinned and aside before anything goes in: a restore point
// that's only half there is still kept, and never mistaken for a run's.
func newAsidePoint(target, id string) (string, time.Time) {
	t := freeStamp(target, id, time.Now())
	dir := filepath.Join(target, VersionsDir, id, t.Format(stampFmt))
	pin(target, id, t)
	markStamp(target, id, AsideDir, t.Format(stampFmt))
	return dir, t
}

// keepWait is how long Keep waits for a running backup.
var keepWait = time.Minute

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

// MergeHistory moves the restore points kept in src (a local folder used
// while no backup folder was available) into target's history, where they
// can be restored and are pruned like the rest. Points already in target are
// kept as they are. It does nothing while a backup runs; the next call
// retries.
func MergeHistory(src, target string) error {
	from := filepath.Join(src, VersionsDir)
	if !isDir(from) || paths.Within(src, target) || paths.Within(target, src) {
		return nil
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := merge(from, filepath.Join(target, VersionsDir)); err != nil {
		return err
	}
	removeEmptyDirs(src)
	_ = os.Remove(src) // only if empty
	return nil
}
