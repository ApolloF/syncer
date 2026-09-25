// Package conflict finds and resolves Syncthing conflict copies. When two PCs
// both changed a file, Syncthing keeps the newer one under the real name and
// renames the other to name.sync-conflict-<date>-<time>-<device>.ext. Games
// only load the real name, so a lost copy is easy to miss.
package conflict

import (
	"errors"
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

// Preserve moves a file out of the way into history; rel is the name it
// should be restorable under.
type Preserve func(abs, rel string) error

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
		return keep(copyAbs, origRel)
	}
	if _, err := os.Stat(origAbs); err == nil {
		if err := keep(origAbs, origRel); err != nil {
			return err
		}
	}
	if err := os.Rename(copyAbs, origAbs); err != nil {
		return errors.New("could not swap the files (is the game running?): " + err.Error())
	}
	return nil
}

// StVersionsKeep is a Preserve that moves the file into the folder's own
// .stversions, named the way Syncthing names its versions.
func StVersionsKeep(dir string) Preserve {
	return func(abs, rel string) error {
		ext := filepath.Ext(rel)
		name := strings.TrimSuffix(rel, ext) + "~" + time.Now().Format("20060102-150405") + ext
		dst := filepath.Join(dir, ".stversions", name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Rename(abs, dst)
	}
}
