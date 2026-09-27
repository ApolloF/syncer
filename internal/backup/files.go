package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ApolloF/syncer/internal/fsx"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
)

// Next to its info file, a PC lists the files its last backup of a folder
// left in the backup (<target>\.syncer\<id>\files\<pc>.json), and the info
// file names the list's hash. Google Drive for desktop uploads and downloads
// files one by one, so the backup another PC sees may be half there: a PC
// only takes saves from the backup when every file on the list is there as
// listed. The list sits in a folder of its own, where older Syncer versions
// don't take it for an info file.
const filesDir = "files"

// maxFiles caps a file list; it comes from another PC, so it isn't trusted.
const maxFiles = 8 << 20

// FileEntry is one file of a backup: its path in the folder, size and
// modification time (unix milliseconds).
type FileEntry struct {
	Path  string `json:"p"`
	Size  int64  `json:"s"`
	MTime int64  `json:"m"`
}

func (e FileEntry) time() time.Time { return time.UnixMilli(e.MTime) }

type fileList struct {
	ID    string      `json:"id"`
	Files []FileEntry `json:"files"`
}

func filesPath(target, id, key string) string {
	return filepath.Join(target, InfoDir, id, filesDir, key+".json")
}

// writeFiles writes this PC's list of f's files in the backup and returns
// its hash.
func writeFiles(target, id string, files []FileEntry) (string, error) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	b, err := json.Marshal(fileList{ID: id, Files: files})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	p := filesPath(target, id, hostKey)
	if old, err := os.ReadFile(p); err == nil {
		if s := sha256.Sum256(old); hex.EncodeToString(s[:]) == hash {
			return hash, nil // unchanged: nothing for Drive to upload
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := p + tmpSuffix
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return hash, nil
}

// ReadFiles returns the list of files another PC's backup of in.ID left in
// the backup, as its info file describes it. It fails when the list isn't
// (yet) the one the info file names, or holds a path that could point
// outside the folder.
func ReadFiles(target string, in Info) ([]FileEntry, error) {
	if in.Files == "" || in.key == "" || !paths.ValidID(in.ID) {
		return nil, errors.New("that PC's backup has no file list (an older Syncer)")
	}
	p := filesPath(target, in.ID, in.key)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFiles {
		return nil, errors.New("not a file list")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if s := sha256.Sum256(b); hex.EncodeToString(s[:]) != in.Files {
		return nil, errors.New("the file list hasn't fully arrived yet")
	}
	var l fileList
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.ID != in.ID {
		return nil, errors.New("file list of another folder")
	}
	for _, e := range l.Files {
		if !safeRel(e.Path) || e.Size < 0 {
			return nil, fmt.Errorf("unsafe path in file list: %q", e.Path)
		}
	}
	return l.Files, nil
}

// safeRel reports whether rel names a file inside a folder.
func safeRel(rel string) bool {
	if rel == "" || rel[0] == '/' || rel[0] == '\\' || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.ContainsAny(rel, ":\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

// MirrorMatches reports whether the backup of id holds every listed file as
// listed: Drive has brought all of that PC's backup to this PC.
func MirrorMatches(target, id string, files []FileEntry) bool {
	if len(files) == 0 {
		return false
	}
	for _, e := range files {
		fi, err := os.Stat(filepath.Join(target, id, filepath.FromSlash(e.Path)))
		if err != nil || fi.IsDir() || fi.Size() != e.Size || !sameTime(fi.ModTime(), e.time()) {
			return false
		}
	}
	return true
}

// LocalMatchesIndex reports whether f's files are exactly the ones its last
// backup from this PC saw: nothing changed here since then.
func LocalMatchesIndex(f Folder) bool {
	idx := loadIndex(f.ID)
	if len(idx) == 0 {
		return false
	}
	m := LoadMatcher(f.Path, f.Exclude...)
	n := 0
	same := true
	err := filepath.WalkDir(f.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e, ok := idx[strings.ToLower(filepath.ToSlash(rel))]
		if !ok || e.Size != info.Size() || e.MTime != info.ModTime().UnixNano() {
			same = false
			return filepath.SkipAll
		}
		n++
		return nil
	})
	return err == nil && same && n == len(idx)
}

// TakeFiles copies the listed files from the backup of f into f's folder,
// the saves another PC backed up. Files that already hold the same are left
// alone, and files not on the list stay. Every file is first copied next to
// the folder's saves (into Syncthing's .stversions, which nothing syncs or
// backs up), and only then are they swapped in; if one can't be (the game
// has it open), the ones swapped already are put back. The caller saves the
// current files as a restore point first. It returns how many files changed.
func TakeFiles(ctx context.Context, target string, f Folder, files []FileEntry) (int, error) {
	if !paths.ValidID(f.ID) {
		return 0, fmt.Errorf("unsupported folder id %q", f.ID)
	}
	unlock, err := waitLock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	m := LoadMatcher(f.Path, f.Exclude...)
	stage := filepath.Join(f.Path, ".stversions", ".syncer-take-"+time.Now().Format(stampFmt))
	keepStage := false // an original couldn't be put back: it's still in there
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()

	type swap struct{ rel, staged, to, old string }
	var swaps []swap
	for i, e := range files {
		rel := filepath.FromSlash(e.Path)
		if !safeRel(e.Path) || m.Ignored(rel) || strings.EqualFold(filepath.Base(rel), SteamMarker) {
			continue
		}
		from := filepath.Join(target, f.ID, rel)
		fi, err := os.Stat(from)
		if err != nil || fi.Size() != e.Size || !sameTime(fi.ModTime(), e.time()) {
			return 0, fmt.Errorf("%s changed in the backup meanwhile", e.Path)
		}
		to := filepath.Join(f.Path, rel)
		if same, err := fsx.SameContent(to, from); err == nil && same {
			continue
		}
		staged := filepath.Join(stage, "new", fmt.Sprint(i))
		if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
			return 0, err
		}
		if err := copyFile(from, staged); err != nil {
			return 0, fmt.Errorf("%s: %w", e.Path, err)
		}
		_ = os.Chtimes(staged, fi.ModTime(), fi.ModTime())
		swaps = append(swaps, swap{rel: e.Path, staged: staged, to: to, old: filepath.Join(stage, "old", fmt.Sprint(i))})
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if len(swaps) > 0 {
		_ = os.MkdirAll(filepath.Join(stage, "old"), 0o755)
	}
	var done []swap
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			s := done[i]
			if _, err := os.Stat(s.old); err == nil {
				// Rename replaces the new file with the original.
				if os.Rename(s.old, s.to) != nil {
					keepStage = true
				}
			} else {
				_ = os.Remove(s.to)
			}
		}
		if keepStage {
			logx.Printf("taking saves for %s failed and some originals couldn't be put back; they're in %s", f.Label, filepath.Join(stage, "old"))
		}
	}
	for _, s := range swaps {
		if _, err := os.Stat(s.to); err == nil {
			if err := os.Rename(s.to, s.old); err != nil {
				undo()
				return 0, fmt.Errorf("%s: %w (is the game running?)", s.rel, err)
			}
		} else if err := os.MkdirAll(filepath.Dir(s.to), 0o755); err != nil {
			undo()
			return 0, err
		}
		if err := os.Rename(s.staged, s.to); err != nil {
			if _, serr := os.Stat(s.old); serr == nil && os.Rename(s.old, s.to) != nil {
				keepStage = true
			}
			undo()
			return 0, fmt.Errorf("%s: %w", s.rel, err)
		}
		done = append(done, s)
	}
	return len(done), nil
}

// indexFiles records what this run left in the backup, for the file list.
func indexFiles(idx map[string]indexEntry, names map[string]string) []FileEntry {
	out := make([]FileEntry, 0, len(idx))
	for k, e := range idx {
		rel := names[k]
		if rel == "" {
			continue
		}
		out = append(out, FileEntry{Path: filepath.ToSlash(rel), Size: e.Size, MTime: time.Unix(0, e.MTime).UnixMilli()})
	}
	return out
}
