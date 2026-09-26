package backup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Restore copies a folder's backup back to its local path as it was at point
// (zero time = latest backup). Current local files that get overwritten are
// first saved as a new restore point, so a restore can always be undone.
//
// Versions hold the content a file had *before* the stamped run replaced it, so
// the state at point T is: latest backup, overlaid by every version taken at or
// after T, newest first, so older (closer to T) copies win.
func Restore(target string, f Folder, point time.Time) (int, error) {
	if !paths.ValidID(f.ID) {
		return 0, fmt.Errorf("unsupported folder id %q", f.ID)
	}
	unlock, err := lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	src := map[string]string{}  // rel(lower) -> absolute source file
	rels := map[string]string{} // rel(lower) -> rel (original case)
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
	add(filepath.Join(target, f.ID))
	if !point.IsZero() {
		for _, t := range Points(target, f.ID) { // newest first
			if !t.Before(point) {
				add(filepath.Join(target, VersionsDir, f.ID, t.Format(stampFmt)))
			}
		}
	}
	if len(src) == 0 {
		return 0, fmt.Errorf("no backup found for %s", f.Label)
	}

	safety := filepath.Join(target, VersionsDir, f.ID, time.Now().Format(stampFmt))
	kept := false
	n := 0
	for k, from := range src {
		rel := rels[k]
		to := filepath.Join(f.Path, rel)
		if fi, err := os.Stat(to); err == nil && !fi.IsDir() {
			keep := filepath.Join(safety, rel)
			_ = os.MkdirAll(filepath.Dir(keep), 0o755)
			if err := copyFile(to, keep); err != nil {
				return n, fmt.Errorf("could not save current %s: %w", rel, err)
			}
			if !kept {
				keepPoint(safety) // the saves a restore replaced outlive the usual history
				kept = true
			}
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
