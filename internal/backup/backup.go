// Package backup mirrors save folders into a Google Drive for desktop folder.
// Nothing is ever deleted outright: files that change or disappear locally are
// moved to <target>\.versions\<folder>\<timestamp>\ and pruned after KeepDays.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

const (
	VersionsDir = ".versions"
	stampFmt    = "2006-01-02_150405"
	tmpSuffix   = ".syncer-tmp"
)

// ErrBusy means another backup is already running.
var ErrBusy = errors.New("a backup is already running")

// Folder is one save folder to back up.
type Folder struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// Progress is reported while a backup runs.
type Progress struct {
	Folder     string `json:"folder"`
	FolderIdx  int    `json:"folderIdx"`
	Folders    int    `json:"folders"`
	FilesDone  int    `json:"filesDone"`
	Copied     int    `json:"copied"`
	BytesTotal int64  `json:"bytes"`
}

// Options for a run.
type Options struct {
	Target   string
	KeepDays int
	OnProg   func(Progress)
	// Pause is called before each folder and every few seconds while copying;
	// it may block (e.g. while a game is running) until the run may continue.
	Pause func(ctx context.Context)
}

var pauseEvery = 2 * time.Second // var so tests can pause on every file

type indexEntry struct {
	Size  int64 `json:"s"`
	MTime int64 `json:"m"`
}

// Run backs up all folders into opts.Target.
func Run(ctx context.Context, folders []Folder, opts Options) (*store.BackupRun, error) {
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	res := &store.BackupRun{Started: time.Now(), Target: opts.Target}
	if err := os.MkdirAll(opts.Target, 0o755); err != nil {
		return nil, fmt.Errorf("backup folder not reachable: %w", err)
	}
	stamp := time.Now().Format(stampFmt)
	p := Progress{Folders: len(folders)}
	for i, f := range folders {
		if opts.Pause != nil {
			opts.Pause(ctx)
		}
		if ctx.Err() != nil {
			res.Errors = append(res.Errors, stopReason(ctx))
			break
		}
		if !paths.ValidID(f.ID) {
			res.Errors = append(res.Errors, f.Label+": unsupported folder id")
			continue
		}
		p.Folder, p.FolderIdx = f.Label, i+1
		if opts.OnProg != nil {
			opts.OnProg(p)
		}
		if !isDir(f.Path) {
			continue // game not present on this PC
		}
		res.Folders++
		c, v, b, errs := mirror(ctx, f, opts, stamp, &p)
		res.Copied += c
		res.Versions += v
		res.Bytes += b
		res.Errors = append(res.Errors, errs...)
	}
	if opts.KeepDays > 0 {
		prune(opts.Target, opts.KeepDays)
	}
	res.Finished = time.Now()
	res.OK = len(res.Errors) == 0
	return res, nil
}

// stopReason says why a run stopped early: a cause given by the caller (e.g.
// a game kept running) or plain cancellation.
func stopReason(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil && !errors.Is(c, context.Canceled) && !errors.Is(c, context.DeadlineExceeded) {
		return c.Error()
	}
	return "cancelled"
}

func mirror(ctx context.Context, f Folder, opts Options, stamp string, p *Progress) (copied, versioned int, bytes int64, errs []string) {
	target, onProg := opts.Target, opts.OnProg
	lastPause := time.Now()
	dst := filepath.Join(target, f.ID)
	verRoot := filepath.Join(target, VersionsDir, f.ID, stamp)
	m := LoadMatcher(f.Path)
	idx := loadIndex(f.ID)
	newIdx := map[string]indexEntry{}
	seen := map[string]bool{}
	errf := func(format string, a ...any) { errs = append(errs, f.Label+": "+fmt.Sprintf(format, a...)) }

	_ = filepath.WalkDir(f.Path, func(path string, d fs.DirEntry, err error) error {
		if opts.Pause != nil && time.Since(lastPause) >= pauseEvery {
			opts.Pause(ctx)
			lastPause = time.Now()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(f.Path, path)
		if err != nil {
			if rel != "." {
				errf("%s: %v", rel, err)
			}
			return nil
		}
		if rel == "." {
			return nil
		}
		if m.Ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			errf("%s: %v", rel, err)
			return nil
		}
		key := strings.ToLower(filepath.ToSlash(rel))
		seen[key] = true
		cur := indexEntry{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		out := filepath.Join(dst, rel)
		old, known := idx[key]
		if ti, err := os.Stat(out); err == nil && ti.Size() == cur.Size &&
			((known && old == cur) || (!known && sameTime(ti.ModTime(), info.ModTime()))) {
			newIdx[key] = cur
			p.FilesDone++
			return nil
		}
		v, err := copyVersioned(path, out, filepath.Join(verRoot, rel), info.ModTime())
		if err != nil {
			errf("%s: %v", rel, err)
			if known {
				newIdx[key] = old // keep old state so we retry next run
			}
			return nil
		}
		if v {
			versioned++
		}
		newIdx[key] = cur
		copied++
		bytes += cur.Size
		p.FilesDone++
		p.Copied++
		p.BytesTotal += cur.Size
		if onProg != nil && copied%20 == 0 {
			onProg(*p)
		}
		return nil
	})

	// Files gone locally: move their backup copy into versions. If the local
	// folder is suddenly empty, assume something is wrong and keep everything.
	// A cancelled walk hasn't seen every file, so it must not retire anything.
	if len(seen) > 0 && ctx.Err() == nil {
		_ = filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dst, path)
			if strings.HasSuffix(rel, tmpSuffix) {
				_ = os.Remove(path)
				return nil
			}
			if seen[strings.ToLower(filepath.ToSlash(rel))] || m.Ignored(rel) {
				return nil
			}
			if err := moveTo(path, filepath.Join(verRoot, rel)); err != nil {
				errf("retire %s: %v", rel, err)
			} else {
				versioned++
			}
			return nil
		})
		removeEmptyDirs(dst)
	}
	if ctx.Err() == nil {
		saveIndex(f.ID, newIdx)
	}
	if onProg != nil {
		onProg(*p)
	}
	return
}

// copyVersioned copies src to dst atomically; an existing dst is first moved to ver.
func copyVersioned(src, dst, ver string, mtime time.Time) (versioned bool, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	tmp := dst + tmpSuffix
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	_ = os.Chtimes(tmp, mtime, mtime)
	if _, err := os.Stat(dst); err == nil {
		if err := moveTo(dst, ver); err != nil {
			_ = os.Remove(tmp)
			return false, err
		}
		versioned = true
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return versioned, err
	}
	return versioned, nil
}

// sameTime compares mtimes loosely; cloud filesystems round timestamps.
func sameTime(a, b time.Time) bool {
	d := a.Sub(b)
	return d > -2*time.Second && d < 2*time.Second
}

func copyFile(src, dst string) error {
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

func moveTo(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-volume fallback (custom target on another drive).
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails harmlessly when not empty
	}
}

// Points lists restore points (version stamps) for a folder, newest first.
func Points(target, id string) []time.Time {
	es, _ := os.ReadDir(filepath.Join(target, VersionsDir, id))
	var ts []time.Time
	for _, e := range es {
		if t, err := time.ParseInLocation(stampFmt, e.Name(), time.Local); err == nil && e.IsDir() {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].After(ts[j]) })
	return ts
}

func prune(target string, keepDays int) {
	cut := time.Now().AddDate(0, 0, -keepDays)
	ids, _ := os.ReadDir(filepath.Join(target, VersionsDir))
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		for _, t := range Points(target, id.Name()) {
			if t.Before(cut) {
				_ = os.RemoveAll(filepath.Join(target, VersionsDir, id.Name(), t.Format(stampFmt)))
			}
		}
	}
}

// ---- index -------------------------------------------------------------------

func indexPath(id string) string {
	d := filepath.Join(paths.AppDir(), "backup-index")
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, id+".json")
}

func loadIndex(id string) map[string]indexEntry {
	m := map[string]indexEntry{}
	if b, err := os.ReadFile(indexPath(id)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveIndex(id string, m map[string]indexEntry) { _ = store.WriteJSON(indexPath(id), m) }

// ---- lock --------------------------------------------------------------------

// lock takes an exclusive, crash-safe lock: the OS releases it with the process.
func lock() (func(), error) {
	p, _ := syscall.UTF16PtrFromString(filepath.Join(paths.AppDir(), "backup.lock"))
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, ErrBusy
	}
	return func() { syscall.CloseHandle(h) }, nil
}

// Running reports whether a backup currently holds the lock.
func Running() bool {
	u, err := lock()
	if err != nil {
		return true
	}
	u()
	return false
}

// Forget drops Syncer's local bookkeeping for a folder and, with
// deleteBackup, its backup copy and version history under target.
func Forget(target, id string, deleteBackup bool) error {
	if !paths.ValidID(id) {
		return fmt.Errorf("unsupported folder id %q", id)
	}
	if err := os.Remove(indexPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !deleteBackup || target == "" {
		return nil
	}
	for _, d := range []string{filepath.Join(target, id), filepath.Join(target, VersionsDir, id)} {
		if !strictlyInside(target, d) {
			return fmt.Errorf("refusing to delete %s", d)
		}
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

func strictlyInside(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != "." && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hiddenOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	return string(out), err
}
