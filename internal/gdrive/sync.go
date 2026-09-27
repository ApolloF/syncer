package gdrive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/store"
)

// A folder on this PC (the backup, as Syncer writes it) is kept the same as
// a folder at the top of My Drive, both ways: every PC signed in to the same
// account shares that folder, as with Drive for desktop. What was in sync
// last time is remembered, so a file missing on one side was deleted there
// (and goes on the other side too), while a file that's new on one side is
// copied to the other. When both sides changed a file, the newer one wins.

// RootName is the folder at the top of My Drive that backups go into.
const RootName = "GameSaveBackup"

// drive is what Sync needs from Google Drive (a fake in tests).
type drive interface {
	List(ctx context.Context) ([]File, error)
	FindTop(ctx context.Context, name string) ([]File, error)
	Mkdir(ctx context.Context, parent, name string) (File, error)
	Upload(ctx context.Context, parent, name, path string, mtime time.Time) (File, error)
	Update(ctx context.Context, id, path string, mtime time.Time) (File, error)
	Download(ctx context.Context, id string, w io.Writer) error
	Move(ctx context.Context, id, from, to, name string) (File, error)
	Delete(ctx context.Context, id string) error
}

// State is what was in sync after the last sync.
type State struct {
	RootID string                `json:"rootID"`
	Files  map[string]stateEntry `json:"files"` // by lower-case path
	Synced time.Time             `json:"synced,omitzero"`
}

type stateEntry struct {
	Rel       string `json:"rel"` // path as named, forward slashes
	ID        string `json:"id"`
	MD5       string `json:"md5"`
	Size      int64  `json:"size"`
	LocalMod  int64  `json:"lmod"` // local modification time (unix ns)
	LocalSize int64  `json:"lsize"`
}

// statePath is where the state is kept; a variable for tests.
var statePath = func() string { return filepath.Join(storeDir(), "gdrive-state.json") }

func storeDir() string { return filepath.Dir(tokenFile()) }

// LoadState reads the state of the last sync.
func LoadState() State {
	var st State
	if b, err := os.ReadFile(statePath()); err != nil || json.Unmarshal(b, &st) != nil || st.Files == nil {
		st = State{Files: map[string]stateEntry{}}
	}
	return st
}

func saveState(st State) { _ = store.WriteJSON(statePath(), st) }

// ForgetState drops the state (signing out, or another account).
func ForgetState() { _ = os.Remove(statePath()) }

// Report says what a sync did.
type Report struct {
	Up, Down, Moved, DeletedHere, DeletedThere int
	Errors                                     []string
}

// tmpSuffix marks files being downloaded; they're never uploaded.
const tmpSuffix = ".gdrive-tmp"

// massDelete: a sync that would delete more than half of one side (and more
// than this many files) is held back; something is wrong, not a cleanup.
const massDelete = 20

type remoteFile struct {
	File
	rel string
}

type localFile struct {
	rel   string
	size  int64
	mtime time.Time
}

// Sync makes local and the RootName folder in Drive the same.
func Sync(ctx context.Context, d drive, local string) (Report, error) {
	st := LoadState()
	rep, err := syncWith(ctx, d, local, &st)
	if err == nil {
		st.Synced = time.Now()
	}
	saveState(st)
	return rep, err
}

func syncWith(ctx context.Context, d drive, local string, st *State) (Report, error) {
	var rep Report
	if err := os.MkdirAll(local, 0o755); err != nil {
		return rep, err
	}
	all, err := d.List(ctx)
	if err != nil {
		return rep, err
	}
	byID := map[string]File{}
	for _, f := range all {
		byID[f.ID] = f
	}
	if r, ok := byID[st.RootID]; !ok || !r.Folder() {
		root, err := findRoot(ctx, d)
		if err != nil {
			return rep, err
		}
		if root.ID != st.RootID {
			// Another folder than last time (first sync, or it was deleted
			// in Drive): nothing is known to be in sync with it.
			st.RootID, st.Files = root.ID, map[string]stateEntry{}
		}
		byID[root.ID] = root
	}
	dirs, remote := tree(byID, st.RootID)
	here, err := walkLocal(local)
	if err != nil {
		return rep, err
	}
	// One side empty while the last sync saw files: it was lost or not
	// there yet, not emptied on purpose. Copy instead of deleting.
	if len(st.Files) > 0 && (len(here) == 0 || len(remote) == 0) {
		st.Files = map[string]stateEntry{}
	}

	keys := map[string]bool{}
	for k := range st.Files {
		keys[k] = true
	}
	for k := range remote {
		keys[k] = true
	}
	for k := range here {
		keys[k] = true
	}
	var (
		down, up, fresh   []string
		delHere, delThere []string
		sortedKeys        = make([]string, 0, len(keys))
	)
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, k := range sortedKeys {
		s, known := st.Files[k]
		r, there := remote[k]
		l, isHere := here[k]
		changedHere := isHere && (!known || l.size != s.LocalSize || l.mtime.UnixNano() != s.LocalMod)
		changedThere := there && (!known || r.ID != s.ID || r.MD5 != s.MD5)
		switch {
		case isHere && there:
			switch {
			case !changedHere && !changedThere:
			case changedHere && !changedThere:
				up = append(up, k)
			case changedThere && !changedHere:
				down = append(down, k)
			default:
				if sum, err := md5File(filepath.Join(local, filepath.FromSlash(l.rel))); err == nil && sum == r.MD5 {
					st.Files[k] = entry(l, r.File) // the same on both sides
				} else if l.mtime.After(r.Modified) {
					up = append(up, k)
				} else {
					down = append(down, k)
				}
			}
		case isHere:
			if known && !changedHere {
				delHere = append(delHere, k) // deleted in Drive
			} else {
				fresh = append(fresh, k)
			}
		case there:
			if known && !changedThere {
				delThere = append(delThere, k) // deleted here
			} else {
				down = append(down, k)
			}
		default:
			delete(st.Files, k)
		}
	}
	if len(delHere) > massDelete && 2*len(delHere) > len(here) {
		rep.Errors = append(rep.Errors, fmt.Sprintf("not deleting %d files here that are gone from Google Drive: that's most of the backup", len(delHere)))
		delHere = nil
	}
	if len(delThere) > massDelete && 2*len(delThere) > len(remote) {
		rep.Errors = append(rep.Errors, fmt.Sprintf("not deleting %d files in Google Drive that are gone here: that's most of the backup", len(delThere)))
		delThere = nil
	}

	fail := func(k string, err error) {
		if ctx.Err() == nil {
			rep.Errors = append(rep.Errors, k+": "+err.Error())
		}
	}
	ensure := func(rel string) (string, error) { return ensureDir(ctx, d, st.RootID, dirs, path.Dir(rel)) }

	// A file that moved here (history: a replaced save goes into .versions)
	// moves in Drive too, instead of going up again.
	gone := map[string][]string{} // size|md5 -> keys deleted here
	for _, k := range delThere {
		r := remote[k]
		gone[fmt.Sprint(r.Size, "|", r.MD5)] = append(gone[fmt.Sprint(r.Size, "|", r.MD5)], k)
	}
	var created []string
	for _, k := range fresh {
		if ctx.Err() != nil {
			break
		}
		l := here[k]
		p := filepath.Join(local, filepath.FromSlash(l.rel))
		if len(gone) > 0 && l.size > 0 {
			if sum, err := md5File(p); err == nil {
				key := fmt.Sprint(l.size, "|", sum)
				if from := gone[key]; len(from) > 0 {
					gone[key] = from[1:]
					r := remote[from[0]]
					parent, err := ensure(l.rel)
					if err == nil {
						var f File
						if f, err = d.Move(ctx, r.ID, firstParent(r.File), parent, path.Base(l.rel)); err == nil {
							delete(st.Files, from[0])
							st.Files[k] = entry(l, f)
							delThere = remove(delThere, from[0])
							rep.Moved++
							continue
						}
					}
					fail(l.rel, err)
				}
			}
		}
		created = append(created, k)
	}
	for _, k := range created {
		if ctx.Err() != nil {
			break
		}
		l := here[k]
		parent, err := ensure(l.rel)
		if err != nil {
			fail(l.rel, err)
			continue
		}
		f, err := d.Upload(ctx, parent, path.Base(l.rel), filepath.Join(local, filepath.FromSlash(l.rel)), l.mtime)
		if err != nil {
			fail(l.rel, err)
			continue
		}
		st.Files[k] = entry(l, f)
		rep.Up++
	}
	for _, k := range up {
		if ctx.Err() != nil {
			break
		}
		l, r := here[k], remote[k]
		f, err := d.Update(ctx, r.ID, filepath.Join(local, filepath.FromSlash(l.rel)), l.mtime)
		if err != nil {
			fail(l.rel, err)
			continue
		}
		st.Files[k] = entry(l, f)
		rep.Up++
	}
	for _, k := range down {
		if ctx.Err() != nil {
			break
		}
		r := remote[k]
		l, err := download(ctx, d, local, r)
		if err != nil {
			fail(r.rel, err)
			continue
		}
		st.Files[k] = entry(l, r.File)
		rep.Down++
	}
	for _, k := range delThere {
		if ctx.Err() != nil {
			break
		}
		if err := d.Delete(ctx, remote[k].ID); err != nil {
			fail(remote[k].rel, err)
			continue
		}
		delete(st.Files, k)
		rep.DeletedThere++
	}
	for _, k := range delHere {
		l := here[k]
		if err := os.Remove(filepath.Join(local, filepath.FromSlash(l.rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fail(l.rel, err)
			continue
		}
		delete(st.Files, k)
		rep.DeletedHere++
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	return rep, nil
}

func entry(l localFile, f File) stateEntry {
	return stateEntry{Rel: l.rel, ID: f.ID, MD5: f.MD5, Size: f.Size, LocalMod: l.mtime.UnixNano(), LocalSize: l.size}
}

func remove(ks []string, k string) []string {
	out := ks[:0]
	for _, x := range ks {
		if x != k {
			out = append(out, x)
		}
	}
	return out
}

func firstParent(f File) string {
	if len(f.Parents) > 0 {
		return f.Parents[0]
	}
	return ""
}

// findRoot returns the RootName folder at the top of My Drive (the oldest,
// if there are several), creating it when there's none.
func findRoot(ctx context.Context, d drive) (File, error) {
	fs, err := d.FindTop(ctx, RootName)
	if err != nil {
		return File{}, err
	}
	if len(fs) > 0 {
		return fs[0], nil
	}
	return d.Mkdir(ctx, "root", RootName)
}

// tree turns Drive's flat list into paths below root: folders by lower-case
// path, and files by lower-case path (the newest wins when Drive holds two
// files of one name).
func tree(byID map[string]File, root string) (dirs map[string]string, files map[string]remoteFile) {
	dirs, files = map[string]string{".": root}, map[string]remoteFile{}
	var relOf func(id string, depth int) (string, bool)
	memo := map[string]string{}
	relOf = func(id string, depth int) (string, bool) {
		if id == root {
			return ".", true
		}
		if r, ok := memo[id]; ok {
			return r, r != ""
		}
		f, ok := byID[id]
		if !ok || depth > 64 || len(f.Parents) == 0 || !validName(f.Name) {
			memo[id] = ""
			return "", false
		}
		p, ok := relOf(f.Parents[0], depth+1)
		if !ok {
			memo[id] = ""
			return "", false
		}
		r := path.Join(p, f.Name)
		memo[id] = r
		return r, true
	}
	for id, f := range byID {
		if id == root {
			continue
		}
		rel, ok := relOf(id, 0)
		if !ok {
			continue
		}
		k := strings.ToLower(rel)
		if f.Folder() {
			if old, dup := dirs[k]; !dup || byID[old].Created.After(f.Created) {
				dirs[k] = id
			}
			continue
		}
		if old, dup := files[k]; dup && !f.Modified.After(old.Modified) {
			continue
		}
		files[k] = remoteFile{File: f, rel: rel}
	}
	// A file below a folder that lost to a same-named older one isn't
	// reachable the way paths are resolved here; skip it.
	for k, f := range files {
		if d := path.Dir(k); dirs[d] != firstParent(f.File) {
			delete(files, k)
		}
	}
	return dirs, files
}

// validName rejects names that can't be a file name here, or could step out
// of the folder.
func validName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, `/\:*?"<>|`+"\x00")
}

// ensureDir returns the Drive folder for rel (relative to root), creating
// the folders on the way.
func ensureDir(ctx context.Context, d drive, root string, dirs map[string]string, rel string) (string, error) {
	if rel == "." || rel == "" {
		return root, nil
	}
	k := strings.ToLower(rel)
	if id, ok := dirs[k]; ok {
		return id, nil
	}
	parent, err := ensureDir(ctx, d, root, dirs, path.Dir(rel))
	if err != nil {
		return "", err
	}
	f, err := d.Mkdir(ctx, parent, path.Base(rel))
	if err != nil {
		return "", err
	}
	dirs[k] = f.ID
	return f.ID, nil
}

func walkLocal(root string) (map[string]localFile, error) {
	out := map[string]localFile{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, tmpSuffix) || strings.HasSuffix(name, ".syncer-tmp") || name == "desktop.ini" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		out[strings.ToLower(rel)] = localFile{rel: rel, size: fi.Size(), mtime: fi.ModTime()}
		return nil
	})
	return out, err
}

// download copies a Drive file to its place below local, with its time.
func download(ctx context.Context, d drive, local string, r remoteFile) (localFile, error) {
	dst := filepath.Join(local, filepath.FromSlash(r.rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return localFile{}, err
	}
	tmp := dst + tmpSuffix
	f, err := os.Create(tmp)
	if err != nil {
		return localFile{}, err
	}
	h := md5.New()
	err = d.Download(ctx, r.ID, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && r.MD5 != "" && hex.EncodeToString(h.Sum(nil)) != r.MD5 {
		err = errors.New("download damaged (checksum differs)")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return localFile{}, err
	}
	_ = os.Chtimes(tmp, r.Modified, r.Modified)
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return localFile{}, err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return localFile{}, err
	}
	return localFile{rel: r.rel, size: fi.Size(), mtime: fi.ModTime()}, nil
}

func md5File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
