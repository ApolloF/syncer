// Package conflict finds and resolves Syncthing conflict copies. When two PCs
// both changed a file, Syncthing keeps the newer one under the real name and
// renames the other to name.sync-conflict-<date>-<time>-<device>.ext. Games
// only load the real name, so a lost copy is easy to miss.
package conflict

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Conflict is one conflict copy next to the file it lost against.
type Conflict struct {
	Rel          string    `json:"rel"`          // the real file (relative to the folder)
	Copy         string    `json:"copy"`         // the conflict copy
	Device       string    `json:"device"`       // short id of the PC that made the copy
	DeviceName   string    `json:"deviceName"`   // filled in by the caller when known
	Size         int64     `json:"size"`         // of the real file (0 if missing)
	Modified     time.Time `json:"modified"`     // of the real file
	Missing      bool      `json:"missing"`      // the real file no longer exists
	CopySize     int64     `json:"copySize"`     // of the conflict copy
	CopyModified time.Time `json:"copyModified"` // of the conflict copy
}

var re = regexp.MustCompile(`^(.*)\.sync-conflict-(\d{8}-\d{6})-([A-Z0-9]{7})(.*)$`)

// Parse splits a conflict copy's file name into the original name and the
// short device id. ok is false for ordinary files.
func Parse(name string) (orig, device string, ok bool) {
	m := re.FindStringSubmatch(name)
	if m == nil || m[1] == "" {
		return "", "", false
	}
	return m[1] + m[4], m[3], true
}

// skipDir are Syncthing's own folders; copies in there are history already.
func skipDir(name string) bool {
	return strings.EqualFold(name, ".stversions") || strings.EqualFold(name, ".stfolder")
}

// Find lists the conflict copies in dir, oldest real file first.
func Find(dir string) []Conflict {
	var out []Conflict
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		orig, dev, ok := Parse(d.Name())
		if !ok {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		c := Conflict{Copy: rel, Rel: filepath.Join(filepath.Dir(rel), orig), Device: dev}
		if fi, err := d.Info(); err == nil {
			c.CopySize, c.CopyModified = fi.Size(), fi.ModTime()
		}
		if fi, err := os.Stat(filepath.Join(dir, c.Rel)); err == nil && !fi.IsDir() {
			c.Size, c.Modified = fi.Size(), fi.ModTime()
		} else {
			c.Missing = true
		}
		out = append(out, c)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Copy < out[j].Copy
	})
	return out
}

// Count returns how many conflict copies dir holds.
func Count(dir string) int { return len(Find(dir)) }

// Preserve puts a file into history; rel is the name it should be restorable
// under. With move the file goes there, otherwise a copy does and the file
// stays where it is.
type Preserve func(abs, rel string, move bool) error

// Resolve settles one conflict copy (copyRel, relative to dir). With useCopy
// the copy becomes the real file and the current real file goes to history;
// otherwise the copy itself goes to history. Nothing is deleted.
func Resolve(dir, copyRel string, useCopy bool, keep Preserve) error {
	copyRel = filepath.Clean(copyRel)
	if filepath.IsAbs(copyRel) || copyRel == ".." || strings.HasPrefix(copyRel, ".."+string(filepath.Separator)) {
		return errors.New("invalid file")
	}
	orig, _, ok := Parse(filepath.Base(copyRel))
	if !ok {
		return errors.New("not a conflict copy")
	}
	copyAbs := filepath.Join(dir, copyRel)
	if fi, err := os.Stat(copyAbs); err != nil || fi.IsDir() {
		return errors.New("that copy is gone — it may already be resolved on another PC")
	}
	origRel := filepath.Join(filepath.Dir(copyRel), orig)
	origAbs := filepath.Join(dir, origRel)
	if !useCopy {
		return keep(copyAbs, origRel, true)
	}
	// The current file is copied into history, then the copy replaces it in
	// one rename: if that fails (the game has the file open), the current
	// file is still there. Moving it away first would leave the save missing
	// here, and Syncthing would pass that on to the other PCs as a delete.
	if _, err := os.Stat(origAbs); err == nil {
		if err := keep(origAbs, origRel, false); err != nil {
			return err
		}
	}
	if err := os.Rename(copyAbs, origAbs); err != nil {
		return errors.New("could not swap the files (is the game running?): " + err.Error())
	}
	return nil
}

// StVersionsKeep is a Preserve that puts the file into the folder's own
// .stversions, named the way Syncthing names its versions.
func StVersionsKeep(dir string) Preserve {
	return func(abs, rel string, move bool) error {
		ext := filepath.Ext(rel)
		name := strings.TrimSuffix(rel, ext) + "~" + time.Now().Format("20060102-150405") + ext
		dst := filepath.Join(dir, ".stversions", name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if move {
			return os.Rename(abs, dst)
		}
		return CopyFile(abs, dst)
	}
}

// CopyFile copies src to dst, keeping its modification time.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// FromDevice returns the conflict copies that the PC with short id device
// made, for files that have no other conflict copy: taking that PC's version
// of a whole save means using each of them. A file with copies from several
// PCs is left for the user to settle one by one.
func FromDevice(cs []Conflict, device string) []Conflict {
	n := map[string]int{}
	for _, c := range cs {
		n[strings.ToLower(c.Rel)]++
	}
	var out []Conflict
	for _, c := range cs {
		if c.Device == device && n[strings.ToLower(c.Rel)] == 1 {
			out = append(out, c)
		}
	}
	return out
}

// DropIdentical removes the conflict copies in dir that hold exactly what the
// real file holds: two PCs made the same change (for example one took the
// other's save from the backup while it was offline), so there is nothing to
// choose. It returns how many went.
func DropIdentical(dir string, same func(a, b string) (bool, error)) int {
	n := 0
	for _, c := range Find(dir) {
		if c.Missing || c.Size != c.CopySize {
			continue
		}
		cp, real := filepath.Join(dir, c.Copy), filepath.Join(dir, c.Rel)
		if ok, err := same(cp, real); err == nil && ok && os.Remove(cp) == nil {
			n++
		}
	}
	return n
}
