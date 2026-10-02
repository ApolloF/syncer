// Package backup mirrors save folders into a Google Drive for desktop folder.
// Files that change or disappear locally are never deleted outright: they're
// moved to <target>\.versions\<folder>\<timestamp>\, and that history is
// thinned as it ages and pruned after KeepDays (see prune.go).
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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

const (
	VersionsDir = ".versions"
	stampFmt    = "2006-01-02_150405"
	tmpSuffix   = ".syncer-tmp"
)

// Stamp names a restore point made at t (a folder in .versions\<id>).
func Stamp(t time.Time) string { return t.Format(stampFmt) }

// ErrBusy means another backup is already running.
var ErrBusy = errors.New("a backup is already running")

// Folder is one save folder to back up.
type Folder struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Path    string   `json:"path"`
	Exclude []string `json:"exclude,omitempty"` // the game's exclusions (ignore patterns)
	// Solo: only this PC backs it up (backed up only, not synced), so every
	// file in its backup is this PC's.
	Solo bool `json:"-"`
	// Scoped limits the backup to the files in Only (paths relative to
	// Path, forward slashes): a folder that holds much more than what
	// Syncer looks after, like a game folder with deployed mods in it.
	// Scoped with no files backs up nothing.
	Scoped bool     `json:"scoped,omitempty"`
	Only   []string `json:"only,omitempty"`
	// From is the backup this folder's saves came from (the game an account
	// folder was split from, or merged back out of). A file that backup holds
	// unchanged is moved over instead of uploaded again.
	From string `json:"-"`
}

// scope is a Scoped folder's files and the folders they are in (lower-cased).
type scope struct {
	on          bool
	files, dirs map[string]bool
}

func (f Folder) scope() scope {
	sc := scope{on: f.Scoped, files: map[string]bool{}, dirs: map[string]bool{}}
	for _, rel := range f.Only {
		k := strings.ToLower(filepath.ToSlash(rel))
		sc.files[k] = true
		for d := path.Dir(k); d != "." && d != "/"; d = path.Dir(d) {
			sc.dirs[d] = true
		}
	}
	return sc
}

// skip reports whether rel (a file, or a folder when dir) is outside the scope.
func (sc scope) skip(rel string, dir bool) bool {
	if !sc.on {
		return false
	}
	k := strings.ToLower(filepath.ToSlash(rel))
	if dir {
		return !sc.dirs[k]
	}
	return !sc.files[k]
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
	// Device is this PC's Syncthing device id, recorded in the info files so
	// other PCs can tell whether this PC is online.
	Device string
	// List, if set, gives the folders again once the lock is held (their
	// paths can change until then).
	List func() []Folder
}

var pauseEvery = 2 * time.Second // var so tests can pause on every file

// RejectedFor lists the versions of a folder's files decided against, here
// or on another PC. The app sets it to take other PCs' word into account too.
var RejectedFor = func(id string) []store.Rejected { return store.LoadState().RejectedIn(id) }

type indexEntry struct {
	Size  int64 `json:"s"`
	MTime int64 `json:"m"`
	// O names the PC the file was last changed on when it was backed up
	// ("" = unknown), for the restore point it goes into once replaced.
	O string `json:"o,omitempty"`
}

func (e indexEntry) same(o indexEntry) bool { return e.Size == o.Size && e.MTime == o.MTime }

// Run backs up all folders into opts.Target.
func Run(ctx context.Context, folders []Folder, opts Options) (*store.BackupRun, error) {
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if opts.List != nil {
		folders = opts.List()
	}

	res := &store.BackupRun{Started: time.Now(), Target: opts.Target}
	if err := os.MkdirAll(opts.Target, 0o755); err != nil {
		return nil, fmt.Errorf("backup folder not reachable: %w", err)
	}
	stamp := time.Now().Format(stampFmt)
	p := Progress{Folders: len(folders)}
	ids := map[string]bool{}
	for _, f := range folders {
		ids[f.ID] = true
	}
	for i, f := range folders {
		// A backup still in use here keeps its files.
		if f.From == f.ID || ids[f.From] || !paths.ValidID(f.From) {
			f.From = ""
		}
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
		c, v, b, newest, held, files, errs := mirror(ctx, f, opts, stamp, &p)
		if held > 0 {
			res.Held = append(res.Held, fmt.Sprintf("%s: %d file(s)", f.Label, held))
		}
		res.Copied += c
		res.Versions += v
		res.Bytes += b
		res.Errors = append(res.Errors, errs...)
		if len(errs) == 0 && ctx.Err() == nil {
			res.Backed = append(res.Backed, f.ID)
			// A backup that left another PC's newer files in place isn't
			// this PC's saves: no file list then (other PCs don't take
			// from it), only how new the saves here are.
			hash := ""
			if held == 0 {
				var err error
				if hash, err = writeFiles(opts.Target, f.ID, files); err != nil {
					res.Errors = append(res.Errors, f.Label+": file list: "+err.Error())
				}
			}
			writeInfo(opts.Target, f, newest, opts.Device, hash, opts.KeepDays)
		}
	}
	// A stopped run leaves pruning to the next one.
	if opts.KeepDays > 0 && ctx.Err() == nil {
		now := time.Now()
		if err := clockErr(ctx, now); err != nil {
			res.NotPruned = err.Error()
			logx.Printf("old versions not pruned: %v", err)
		} else {
			prune(opts.Target, opts.KeepDays, now)
		}
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

// mirror backs up one folder. newest is the newest save file's time.
//
// Every PC that syncs a folder backs it up into the same place, so the backup
// may hold a file another PC wrote. A PC that hasn't caught up yet (it was
// off while another one played, and backs up at logon before Syncthing has
// brought the new saves over) must not put its older saves over the newer
// ones there, nor move away files it never had. Such files are held: left as
// they are and counted in held. A file this PC wrote itself (the backup
// holds what its index says) is still replaced, also by an older one, as
// after restoring an older save.
func mirror(ctx context.Context, f Folder, opts Options, stamp string, p *Progress) (copied, versioned int, bytes int64, newest time.Time, held int, files []FileEntry, errs []string) {
	target, onProg := opts.Target, opts.OnProg
	lastPause := time.Now()
	dst := filepath.Join(target, f.ID)
	verRoot := filepath.Join(target, VersionsDir, f.ID, stamp)
	m := LoadMatcher(f.Path, f.Exclude...)
	sc := f.scope()
	idx := loadIndex(f.ID)
	newIdx := map[string]indexEntry{}
	seen := map[string]bool{}
	added := 0                      // files new to the backup
	names := map[string]string{}    // key -> rel as named
	org := newOrigins(f.ID, f.Solo) // where the files this run versions came from
	// A version decided against (another PC's copy in the backup) isn't
	// held over the version kept here.
	rejected := RejectedFor(f.ID)
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
		if m.Ignored(rel) || sc.skip(rel, d.IsDir()) {
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
		names[key] = rel
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		cur := indexEntry{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		out := filepath.Join(dst, rel)
		old, known := idx[key]
		prev := "" // where the backup's copy came from, if this PC put it there
		if ti, err := os.Stat(out); err == nil {
			if ti.Size() == cur.Size && ((known && old.same(cur)) || sameTime(ti.ModTime(), info.ModTime())) {
				if cur.O = old.O; !known || !old.same(cur) || cur.O == "" {
					cur.O = org.lookup(rel)
				}
				newIdx[key] = cur // already there
				p.FilesDone++
				return nil
			}
			ours := f.Solo || (known && ti.Size() == old.Size && sameTime(ti.ModTime(), time.Unix(0, old.MTime)))
			if ours && known {
				prev = old.O
			}
			if !ours && ti.ModTime().After(info.ModTime().Add(2*time.Second)) &&
				!store.IsRejected(rejected, rel, ti.ModTime()) {
				held++ // another PC's newer save
				if known {
					newIdx[key] = old
				}
				p.FilesDone++
				return nil
			}
		} else if f.From != "" && adopt(filepath.Join(target, f.From, rel), out, info) {
			added++
			cur.O = org.lookup(rel)
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
			org.add(prev)
		} else {
			added++
		}
		cur.O = org.lookup(rel)
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
		type goner struct{ path, rel, origin string }
		var gone []goner
		total := 0 // files in the backup
		_ = filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dst, path)
			if strings.HasSuffix(rel, tmpSuffix) {
				_ = os.Remove(path)
				return nil
			}
			key := strings.ToLower(filepath.ToSlash(rel))
			if m.Ignored(rel) {
				return nil
			}
			total++
			if seen[key] {
				return nil
			}
			old, known := idx[key]
			if !known && !f.Solo {
				held++ // another PC's file this PC hasn't had yet
				return nil
			}
			gone = append(gone, goner{path, rel, old.O})
			return nil
		})
		retired := 0
		for _, g := range gone {
			if err := moveTo(g.path, filepath.Join(verRoot, g.rel)); err != nil {
				errf("retire %s: %v", g.rel, err)
			} else {
				retired++
				org.add(g.origin)
			}
		}
		versioned += retired
		// Half or more of the backup gone at once, with fewer files coming
		// in, looks like an accident more than like play: an uninstaller
		// took the save but left a settings file, say. So the point holding
		// them is pinned: kept for a year, however short the history is kept
		// otherwise. A game that saves under new names each time brings in
		// as many files as went, so its churn isn't pinned.
		if before := total - added; retired > 0 && len(gone) > added && 2*len(gone) >= before {
			pinStamp(target, f.ID, stamp)
		}
		removeEmptyDirs(dst)
	}
	if versioned > 0 {
		org.save(target, stamp)
	}
	if ctx.Err() == nil {
		saveIndex(f.ID, newIdx)
		files = indexFiles(newIdx, names)
	}
	if onProg != nil {
		onProg(*p)
	}
	return
}

// adopt moves src, a file in the backup a folder's saves came from, to dst
// when it is the local file unchanged (same size and time). Within the backup
// folder that's a rename, which Google Drive does without uploading anything.
func adopt(src, dst string, local fs.FileInfo) bool {
	si, err := os.Stat(src)
	if err != nil || !si.Mode().IsRegular() || si.Size() != local.Size() || !sameTime(si.ModTime(), local.ModTime()) {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false
	}
	return os.Rename(src, dst) == nil
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
	if _, err := os.Lstat(dst); err == nil {
		// Two versions of the same file in one restore point (e.g. a file
		// kept and then backed up in the same second): keep both.
		if err := os.Rename(dst, fmt.Sprintf("%s.syncer-kept-%d", dst, time.Now().UnixNano())); err != nil {
			return err
		}
	}
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

// IndexSize reports how much of a folder is in its backup (as of its last
// backup, from the local index: no need to read the backup folder).
func IndexSize(id string) (bytes int64, files int) {
	if !paths.ValidID(id) {
		return 0, 0
	}
	for _, e := range loadIndex(id) {
		bytes += e.Size
		files++
	}
	return bytes, files
}

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

// Lock takes the backup lock (ErrBusy while a backup or restore runs), so a
// caller can make several changes that no backup may interleave with.
func Lock() (unlock func(), err error) { return lock() }

// Forget drops Syncer's local bookkeeping for a folder (and this PC's info
// file next to its backup) and, with deleteBackup, its backup copy, version
// history and info files under target. The caller must hold Lock, so a
// running backup can't recreate what was deleted.
func Forget(target, id string, deleteBackup bool) error {
	if !paths.ValidID(id) {
		return fmt.Errorf("unsupported folder id %q", id)
	}
	if err := os.Remove(indexPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if target == "" {
		return nil
	}
	if !deleteBackup {
		forgetInfo(target, id)
		return nil
	}
	for _, d := range []string{filepath.Join(target, id), filepath.Join(target, VersionsDir, id), filepath.Join(target, InfoDir, id)} {
		if !strictlyInside(target, d) {
			return fmt.Errorf("refusing to delete %s", d)
		}
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

// CopyHistory seeds folder to's backup and version history from folder
// from's, so a game that stops syncing (and gets its own backup id) keeps its
// restore points. The source is left alone (other PCs may still back it up)
// and files already under to are never overwritten.
func CopyHistory(ctx context.Context, target, from, to string) error {
	return copyHistory(ctx, target, from, to, true)
}

// CopyVersions is CopyHistory without the backup copy itself: only the
// restore points. For a folder whose own backup brings its files over from
// from's (Folder.From) rather than through a slow copy within the backup.
func CopyVersions(ctx context.Context, target, from, to string) error {
	return copyHistory(ctx, target, from, to, false)
}

func copyHistory(ctx context.Context, target, from, to string, mirror bool) error {
	if !paths.ValidID(from) || !paths.ValidID(to) {
		return errors.New("unsupported folder id")
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	trees := [][2]string{{filepath.Join(VersionsDir, from), filepath.Join(VersionsDir, to)}}
	if mirror {
		trees = append([][2]string{{from, to}}, trees...)
	}
	for _, d := range trees {
		src, dst := filepath.Join(target, d[0]), filepath.Join(target, d[1])
		err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == src {
					return filepath.SkipAll // nothing backed up yet
				}
				return err
			}
			if !e.Type().IsRegular() || strings.HasSuffix(p, tmpSuffix) {
				return nil
			}
			rel, _ := filepath.Rel(src, p)
			out := filepath.Join(dst, rel)
			if _, err := os.Lstat(out); err == nil {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			// Through a temporary file: one cut off halfway would be
			// skipped as already copied next time.
			tmp := out + tmpSuffix
			if err := copyFile(p, tmp); err != nil {
				_ = os.Remove(tmp)
				return err
			}
			if err := os.Chtimes(tmp, info.ModTime(), info.ModTime()); err != nil {
				_ = os.Remove(tmp)
				return err
			}
			return os.Rename(tmp, out)
		})
		if err != nil {
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
