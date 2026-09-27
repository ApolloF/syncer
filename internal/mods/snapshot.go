package mods

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Before a PC applies deployed mods, it copies every file the update will
// replace or remove (and its plugin lists) into a snapshot on this PC, and
// notes the files it will add. Rolling back puts the copies back and
// removes the added files, so the game folder is exactly as it was.

// Snapshot describes one snapshot.
type Snapshot struct {
	Stamp   string    `json:"stamp"`
	Folder  string    `json:"folder"`
	Target  string    `json:"target"` // the deployment folder it belongs to
	Created time.Time `json:"created"`
	Gen     int64     `json:"gen"`     // inventory generation applied before it
	Copied  []string  `json:"copied"`  // files saved (relative to Target)
	Added   []string  `json:"added"`   // files the update adds: removed on rollback
	Bytes   int64     `json:"bytes"`   // size of the copies
	Plugins []string  `json:"plugins"` // plugin lists saved (names)
	// NoPlugins are plugin lists that didn't exist: removed on rollback.
	NoPlugins []string `json:"noPlugins,omitempty"`
	Game      string   `json:"game"`
}

// snapshotRoot is where snapshots are kept (a variable for tests).
var snapshotRoot = func() string { return filepath.Join(paths.Root(paths.Local), "Syncer", "mod-snapshots") }

// SnapshotDir is the folder of one snapshot.
func SnapshotDir(folder, stamp string) string { return filepath.Join(snapshotRoot(), folder, stamp) }

// TakeSnapshot copies the files rels (relative to target) that exist, and
// game's plugin lists, and records added as the files an update will add
// and prev as the inventory applied before it (nil for none).
func TakeSnapshot(folder, game, target string, prev *Inventory, rels, added []string) (Snapshot, error) {
	var gen int64
	if prev != nil {
		gen = prev.Gen
	}
	if !paths.ValidID(folder) {
		return Snapshot{}, errors.New("bad folder id")
	}
	stamp := time.Now().UTC().Format("20060102-150405.000")
	dir := SnapshotDir(folder, stamp)
	sn := Snapshot{Stamp: stamp, Folder: folder, Target: target, Created: time.Now().UTC(), Gen: gen, Added: added, Game: game}
	fail := func(err error) (Snapshot, error) {
		_ = os.RemoveAll(dir)
		return Snapshot{}, fmt.Errorf("couldn't save the files it replaces: %w", err)
	}
	for _, rel := range rels {
		if !ValidInvRel(rel) {
			continue
		}
		src := filepath.Join(target, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if err != nil {
			continue // not here: nothing to save
		}
		if !fi.Mode().IsRegular() {
			return fail(fmt.Errorf("%s isn't a plain file", rel))
		}
		if err := copyFile(src, filepath.Join(dir, "files", filepath.FromSlash(rel)), fi.ModTime()); err != nil {
			return fail(err)
		}
		sn.Copied = append(sn.Copied, rel)
		sn.Bytes += fi.Size()
	}
	for name := range pluginListNames {
		p := PluginListPath(game, name)
		if p == "" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			sn.NoPlugins = append(sn.NoPlugins, name)
			continue
		}
		if err := copyFile(p, filepath.Join(dir, "plugins", name), fi.ModTime()); err != nil {
			return fail(err)
		}
		sn.Plugins = append(sn.Plugins, name)
	}
	if prev != nil {
		b, _ := json.Marshal(prev)
		if err := os.WriteFile(filepath.Join(dir, "inventory.json"), b, 0o644); err != nil {
			return fail(err)
		}
	}
	b, _ := json.MarshalIndent(sn, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), b, 0o644); err != nil {
		return fail(err)
	}
	return sn, nil
}

// Inventory is the inventory that was applied when the snapshot was taken
// (nil if none was).
func (sn Snapshot) Inventory() *Inventory {
	b, err := os.ReadFile(filepath.Join(SnapshotDir(sn.Folder, sn.Stamp), "inventory.json"))
	if err != nil {
		return nil
	}
	var inv Inventory
	if json.Unmarshal(b, &inv) != nil {
		return nil
	}
	return &inv
}

// Snapshots lists a folder's snapshots, newest first.
func Snapshots(folder string) []Snapshot {
	if !paths.ValidID(folder) {
		return nil
	}
	es, _ := os.ReadDir(filepath.Join(snapshotRoot(), folder))
	var out []Snapshot
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(snapshotRoot(), folder, e.Name(), "snapshot.json"))
		if err != nil {
			continue
		}
		var sn Snapshot
		if json.Unmarshal(b, &sn) == nil && sn.Stamp == e.Name() {
			out = append(out, sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stamp > out[j].Stamp })
	return out
}

// Rollback puts a snapshot back into target: it restores the saved files
// and plugin lists and removes the files the update added. Only paths
// inside target (and the game's plugin lists) are ever touched.
func Rollback(sn Snapshot, target string) error {
	if !strings.EqualFold(filepath.Clean(sn.Target), filepath.Clean(target)) {
		return errors.New("this snapshot belongs to another folder")
	}
	dir := SnapshotDir(sn.Folder, sn.Stamp)
	var errs []error
	for _, rel := range sn.Added {
		if !ValidInvRel(rel) {
			continue
		}
		p := filepath.Join(target, filepath.FromSlash(rel))
		if !paths.Within(target, p) {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	for _, rel := range sn.Copied {
		if !ValidInvRel(rel) {
			continue
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if !paths.Within(target, dst) {
			continue
		}
		src := filepath.Join(dir, "files", filepath.FromSlash(rel))
		fi, err := os.Stat(src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := copyFile(src, dst, fi.ModTime()); err != nil {
			errs = append(errs, err)
		}
	}
	for _, name := range sn.Plugins {
		if p := PluginListPath(sn.Game, name); p != "" {
			src := filepath.Join(dir, "plugins", name)
			if fi, err := os.Stat(src); err == nil {
				if err := copyFile(src, p, fi.ModTime()); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	for _, name := range sn.NoPlugins {
		if p := PluginListPath(sn.Game, name); p != "" {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// PruneSnapshots keeps a folder's newest keep snapshots and any younger
// than maxAge.
func PruneSnapshots(folder string, keep int, maxAge time.Duration) {
	for i, sn := range Snapshots(folder) {
		if i >= keep && time.Since(sn.Created) > maxAge {
			_ = os.RemoveAll(SnapshotDir(folder, sn.Stamp))
		}
	}
}

// ForgetSnapshots deletes all of a folder's snapshots.
func ForgetSnapshots(folder string) {
	if paths.ValidID(folder) {
		_ = os.RemoveAll(filepath.Join(snapshotRoot(), folder))
	}
}

// copyFile copies src to dst through a temporary file (never leaving a
// half-written dst) and keeps the modification time.
func copyFile(src, dst string, mod time.Time) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".syncer-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = os.Chtimes(tmp, mod, mod)
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
