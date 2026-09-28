package backup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/fsx"
	"github.com/ApolloF/syncer/internal/paths"
)

// Restore copies a folder's backup back to its local path as it was at point
// (zero time = latest backup). Current local files that get overwritten are
// first saved as a new, pinned restore point, so a restore can always be
// undone. Files that already hold what they'd be restored to are left alone.
func Restore(target string, f Folder, point time.Time) (int, error) {
	if !paths.ValidID(f.ID) {
		return 0, fmt.Errorf("unsupported folder id %q", f.ID)
	}
	unlock, err := lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	src, rels := sources(target, f.ID, point)
	if len(src) == 0 {
		return 0, fmt.Errorf("no backup found for %s", f.Label)
	}

	// A point of its own: never add to (or overwrite in) another one made
	// in the same second.
	now := time.Now()
	safety := filepath.Join(target, VersionsDir, f.ID, now.Format(stampFmt))
	for isDir(safety) {
		now = now.Add(time.Second)
		safety = filepath.Join(target, VersionsDir, f.ID, now.Format(stampFmt))
	}
	pinned := false
	org := newOrigins(f.ID, f.Solo)
	defer func() {
		if pinned {
			org.save(target, now.Format(stampFmt))
		}
	}()
	n := 0
	for k, from := range src {
		rel := rels[k]
		to := filepath.Join(f.Path, rel)
		if fi, err := os.Stat(to); err == nil && !fi.IsDir() {
			if same, err := fsx.SameContent(to, from); err == nil && same {
				n++ // already as it was: nothing to save or write
				continue
			}
			if !pinned {
				pin(target, f.ID, now)
				pinned = true
			}
			keep := filepath.Join(safety, rel)
			_ = os.MkdirAll(filepath.Dir(keep), 0o755)
			if err := copyFile(to, keep); err != nil {
				return n, fmt.Errorf("could not save current %s: %w", rel, err)
			}
			org.add(org.lookup(rel))
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return n, err
		}
		fi, err := os.Stat(from)
		if err != nil {
			return n, err
		}
		tmp := to + tmpSuffix
		if err := copyFile(from, tmp); err != nil {
			_ = os.Remove(tmp)
			return n, fmt.Errorf("%s: %w (is the game running?)", rel, err)
		}
		_ = os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
		if err := os.Rename(tmp, to); err != nil {
			_ = os.Remove(tmp)
			return n, fmt.Errorf("%s: %w (is the game running?)", rel, err)
		}
		n++
	}
	return n, nil
}

// sources maps each file (rel, lowercased) of id's backup as it was at point
// (zero time = latest backup) to where its copy is, and to its rel as named.
//
// Versions hold the content a file had *before* the stamped run replaced it, so
// the state at point T is: latest backup, overlaid by every version taken at or
// after T, newest first, so older (closer to T) copies win.
func sources(target, id string, point time.Time) (src, rels map[string]string) {
	src, rels = map[string]string{}, map[string]string{}
	add := func(root string) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			// steam_autocloud.vdf names the account of the PC it was backed
			// up on; this PC's Steam writes its own.
			if err != nil || d.IsDir() || strings.HasSuffix(p, tmpSuffix) || strings.EqualFold(d.Name(), SteamMarker) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			k := strings.ToLower(rel)
			src[k], rels[k] = p, rel
			return nil
		})
	}
	add(filepath.Join(target, id))
	if !point.IsZero() {
		for _, t := range Points(target, id) { // newest first
			if !t.Before(point) {
				add(filepath.Join(target, VersionsDir, id, t.Format(stampFmt)))
			}
		}
	}
	return src, rels
}
