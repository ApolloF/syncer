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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
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
	Account string                `json:"account,omitempty"` // the Google account synced with
	RootID  string                `json:"rootID"`
	Files   map[string]stateEntry `json:"files"` // by lower-case path
	Synced  time.Time             `json:"synced,omitzero"`
	// Forgotten are the folders whose backup this PC deleted on purpose
	// since the last sync (see ForgetFolder).
	Forgotten []string `json:"forgotten,omitempty"`
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

// ForgetState drops the state (another account).
func ForgetState() { _ = os.Remove(statePath()) }

// ForgetFolder records that this PC deleted the backup of folder id on
// purpose, so the next sync deletes it in Drive too, however many files that
// is (see the mass-delete guard in syncWith).
func ForgetFolder(id string) {
	st := LoadState()
	if !slices.Contains(st.Forgotten, strings.ToLower(id)) {
		st.Forgotten = append(st.Forgotten, strings.ToLower(id))
		saveState(st)
	}
}

// forgotten reports whether k (a lower-case path) belongs to a folder whose
// backup was deleted on purpose: its backup, history or info files.
func (st *State) forgotten(k string) bool {
	parts := strings.SplitN(k, "/", 3)
	for _, id := range st.Forgotten {
		if parts[0] == id || len(parts) > 1 && (parts[0] == ".versions" || parts[0] == ".syncer") && parts[1] == id {
			return true
		}
	}
	return false
}

// SetAccount records which Google account the folder is synced with.
func SetAccount(account string) {
	st := LoadState()
	st.Account = account
	saveState(st)
}

// Report says what a sync did.
type Report struct {
	Up, Down, Moved, DeletedHere, DeletedThere int
	Kept                                       int      // losing versions put into history
	Errors                                     []string // files that failed
	Notes                                      []string // worth a log line, not an error
}

// tmpSuffix marks files being downloaded; they're never uploaded.
const tmpSuffix = ".gdrive-tmp"

// massDelete: a sync that would delete more than half of one side (and more
// than this many files) is held back; something is wrong, not a cleanup.
// Saves lost here are also held back game by game: more than half of a
// game's (and more than massDeleteGame files).
const (
	massDelete     = 20
	massDeleteGame = 5
)

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
		st.Synced, st.Forgotten = time.Now(), nil
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
	// Drive can hold two folders of one name (two PCs made it at once, or a
	// retried request made it twice). All of them count as the same folder,
	// at the top as below it; new files go into the oldest.
	roots, err := findRoots(ctx, d)
	if err != nil {
		return rep, err
	}
	isRoot := map[string]bool{}
	for _, r := range roots {
		isRoot[r.ID] = true
		byID[r.ID] = r
	}
	if !isRoot[st.RootID] {
		// Another folder than last time (first sync, or it was deleted in
		// Drive): nothing is known to be in sync with it.
		st.Files = map[string]stateEntry{}
	}
	st.RootID = roots[0].ID
	dirs, remote := tree(byID, isRoot, st.RootID)
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
				// Both changed: the newer one wins, and the other goes into
				// the game's history, as a replaced save would.
				if sum, err := md5File(filepath.Join(local, filepath.FromSlash(l.rel))); err == nil && sum == r.MD5 {
					st.Files[k] = entry(l, r.File) // the same on both sides
				} else if l.mtime.After(r.Modified) {
					if err := keepRemote(ctx, d, local, r); err != nil {
						rep.Errors = append(rep.Errors, r.rel+": keep the other version: "+err.Error())
						continue
					}
					rep.Kept++
					up = append(up, k)
				} else {
					if err := keepLocal(local, l); err != nil {
						rep.Errors = append(rep.Errors, l.rel+": keep this version: "+err.Error())
						continue
					}
					rep.Kept++
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
	// Most of the backups gone from Drive at once (not history, which
	// thinning deletes by the hundreds) looks like an accident in Drive,
	// not another PC at work: they go back up instead of being deleted here.
	var saves []string
	for _, k := range delHere {
		if !strings.HasPrefix(k, versionsPrefix) {
			saves = append(saves, k)
		}
	}
	if n := countSaves(here); len(saves) > massDelete && 2*len(saves) > n {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d backed-up files are gone from Google Drive; putting them back", len(saves)))
		for _, k := range saves {
			delete(st.Files, k)
			delHere = remove(delHere, k)
			fresh = append(fresh, k)
		}
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
	// Most of the backups, or of one game's, gone here at once (a game's
	// backup folder lost or quarantined by antivirus) looks like an accident
	// as well: they come back down instead of being deleted in Drive, where
	// the other PCs would follow. Unless this PC deleted them on purpose (see
	// ForgetFolder).
	var lost []string
	for _, k := range delThere {
		if !strings.HasPrefix(k, versionsPrefix) && !st.forgotten(k) {
			lost = append(lost, k)
		}
	}
	if lost = massLoss(lost, remote); len(lost) > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d backed-up files are gone from this PC; bringing them back from Google Drive", len(lost)))
		for _, k := range lost {
			delete(st.Files, k)
			delThere = remove(delThere, k)
			down = append(down, k)
		}
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

// findRoots returns the RootName folders at the top of My Drive, oldest
// first, creating one when there's none.
func findRoots(ctx context.Context, d drive) ([]File, error) {
	fs, err := d.FindTop(ctx, RootName)
	if err != nil || len(fs) > 0 {
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Created.Before(fs[j].Created) })
		return fs, err
	}
	f, err := d.Mkdir(ctx, "root", RootName)
	return []File{f}, err
}

// tree turns Drive's flat list into paths below the root folders: folders
// by lower-case path (the oldest of same-named ones gets new files), and
// files by lower-case path (the newest wins when Drive holds two).
func tree(byID map[string]File, roots map[string]bool, root string) (dirs map[string]string, files map[string]remoteFile) {
	dirs, files = map[string]string{".": root}, map[string]remoteFile{}
	var relOf func(id string, depth int) (string, bool)
	memo := map[string]string{}
	relOf = func(id string, depth int) (string, bool) {
		if roots[id] {
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
		if roots[id] {
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
	return dirs, files
}

// versionsPrefix starts the paths of the history (restore points).
const versionsPrefix = ".versions/"

// countSaves counts the files that aren't history.
func countSaves[V any](files map[string]V) int {
	n := 0
	for k := range files {
		if !strings.HasPrefix(k, versionsPrefix) {
			n++
		}
	}
	return n
}

// massLoss is the part of lost (saves gone on one side) that is held back:
// all of it when it is most of the saves on the other side (files), else the
// saves of each game (top folder) that lost most of its own.
func massLoss[V any](lost []string, files map[string]V) []string {
	if len(lost) > massDelete && 2*len(lost) > countSaves(files) {
		return lost
	}
	byGame := map[string][]string{}
	for _, k := range lost {
		byGame[game(k)] = append(byGame[game(k)], k)
	}
	saves := map[string]int{}
	for k := range files {
		if !strings.HasPrefix(k, versionsPrefix) {
			saves[game(k)]++
		}
	}
	var out []string
	for g, ks := range byGame {
		if len(ks) > massDeleteGame && 2*len(ks) > saves[g] {
			out = append(out, ks...)
		}
	}
	sort.Strings(out)
	return out
}

// game is the top folder of k: the game a backed-up file belongs to.
func game(k string) string {
	g, _, _ := strings.Cut(k, "/")
	return g
}

// loserPoint makes a new restore point of the game a backed-up save (rel)
// belongs to, for its losing version: a point of its own, pinned and marked
// as saved outside a backup run (see backup.KeepAside), so it never replaces
// a version saved in the same second nor is laid over older restores. Only
// the backups themselves (<id>/…) have one; the history and the info files
// don't need one. It returns the point's folder and the save's path in it.
func loserPoint(local, rel string) (dir, rest string, ok bool, err error) {
	id, rest, found := strings.Cut(rel, "/")
	if !found || id == ".versions" || id == ".syncer" || rest == "" {
		return "", "", false, nil
	}
	dir, err = backup.KeepAside(local, id)
	return dir, rest, err == nil, err
}

// keepLocal copies this PC's version of a file into its game's history
// before Drive's newer one replaces it.
func keepLocal(local string, l localFile) error {
	dir, rest, ok, err := loserPoint(local, l.rel)
	if !ok {
		return err
	}
	to := filepath.Join(dir, filepath.FromSlash(rest))
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := copyLocal(filepath.Join(local, filepath.FromSlash(l.rel)), to); err != nil {
		return err
	}
	_ = os.Chtimes(to, l.mtime, l.mtime)
	return nil
}

// keepRemote saves Drive's version of a file into its game's history (on
// this PC; it goes up with the next sync) before this PC's newer one
// replaces it.
func keepRemote(ctx context.Context, d drive, local string, r remoteFile) error {
	dir, rest, ok, err := loserPoint(local, r.rel)
	if !ok {
		return err
	}
	at, err := filepath.Rel(local, filepath.Join(dir, filepath.FromSlash(rest)))
	if err != nil {
		return err
	}
	_, err = download(ctx, d, local, remoteFile{File: r.File, rel: filepath.ToSlash(at)})
	return err
}

func copyLocal(src, dst string) error {
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
		// A folder that can't be read would look emptied, and its files
		// deleted in Drive: the whole sync waits for it instead.
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, tmpSuffix) || strings.HasSuffix(name, ".syncer-tmp") || name == "desktop.ini" {
			return nil
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone meanwhile
		} else if err != nil {
			return err
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
