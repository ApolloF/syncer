package backup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

// InfoDir holds, next to the backups, one small file per folder and PC
// (<target>\.syncer\<id>\<pc>.json) saying which game a backup is and how new
// its saves are, so other PCs signed in to the same Google account can tell.
// Every PC writes only its own file: Drive never has to merge two PCs' edits.
const InfoDir = ".syncer"

// maxInfo caps an info file; real ones are a few hundred bytes. They can come
// from any PC using the same Google account, so they aren't trusted.
const maxInfo = 64 << 10

// Info is what one PC records about a folder it backs up.
type Info struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Root     string    `json:"root,omitempty"` // portable location (see paths.Portable)
	Rel      string    `json:"rel,omitempty"`
	Host     string    `json:"host"`     // the PC's name
	Newest   time.Time `json:"newest"`   // the newest save file at its last backup
	BackedUp time.Time `json:"backedUp"` // its last backup without errors

	mine bool
}

// Mine reports whether this PC wrote it.
func (in Info) Mine() bool { return in.mine }

// Path resolves where the folder is on this PC. It's only returned when that
// place may hold a save folder at all.
func (in Info) Path() (string, bool) {
	if in.Root == "" {
		return "", false
	}
	p, ok := paths.Resolve(in.Root, in.Rel)
	if !ok || paths.CheckSyncable(p) != nil {
		return "", false
	}
	return p, true
}

var (
	hostName = func() string { h, _ := os.Hostname(); return h }()
	hostKey  = fileKey(hostName)
)

// fileKey turns a PC name into a safe file name.
func fileKey(host string) string {
	k := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if len(k) > 40 {
		k = strings.TrimRight(k[:40], "-")
	}
	if !paths.ValidID(k) {
		return "pc"
	}
	return k
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func infoPath(target, id, key string) string {
	return filepath.Join(target, InfoDir, id, key+".json")
}

// writeInfo records this PC's view of f after a backup. It's rewritten only
// when something changed or once a day, so Drive doesn't upload it every run.
func writeInfo(target string, f Folder, newest time.Time) {
	if !paths.ValidID(f.ID) {
		return
	}
	root, rel, _ := paths.Portable(f.Path)
	in := Info{ID: f.ID, Label: f.Label, Root: root, Rel: rel, Host: hostName, Newest: newest.UTC().Round(time.Second), BackedUp: time.Now().UTC()}
	p := infoPath(target, f.ID, hostKey)
	if old, err := readInfo(p); err == nil && old.Label == in.Label && old.Root == in.Root && old.Rel == in.Rel &&
		old.Host == in.Host && old.Newest.Equal(in.Newest) && time.Since(old.BackedUp) < 24*time.Hour {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = store.WriteJSON(p, in)
	}
}

func readInfo(p string) (Info, error) {
	var in Info
	fi, err := os.Lstat(p)
	if err != nil {
		return in, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxInfo {
		return in, errors.New("not an info file")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return in, err
	}
	return in, json.Unmarshal(b, &in)
}

// ReadInfos returns every PC's info about a folder's backup, most recently
// backed up first. Unreadable, oversized or mislabeled files are skipped.
func ReadInfos(target, id string) []Info {
	if target == "" || !paths.ValidID(id) {
		return nil
	}
	dir := filepath.Join(target, InfoDir, id)
	es, _ := os.ReadDir(dir)
	var out []Info
	for _, e := range es {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		in, err := readInfo(filepath.Join(dir, name))
		if err != nil || in.ID != id {
			continue
		}
		in.Label, in.Host = cleanText(in.Label, 120), cleanText(in.Host, 64)
		in.mine = strings.TrimSuffix(name, ".json") == hostKey
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BackedUp.After(out[j].BackedUp) })
	return out
}

// forgetInfo removes this PC's info file for a folder it no longer backs up.
func forgetInfo(target, id string) {
	if target == "" || !paths.ValidID(id) {
		return
	}
	if err := os.Remove(infoPath(target, id, hostKey)); err == nil || errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(filepath.Join(target, InfoDir, id)) // only if no other PC's file is left
	}
}

// cleanText keeps a name from another PC displayable: no control characters,
// trimmed, at most max runes.
func cleanText(s string, max int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// LabelFromID guesses a game's name from a folder id ("hollow-knight--pc"
// becomes "Hollow knight"), for backups without an info file.
func LabelFromID(id string) string {
	if i := strings.Index(id, "--"); i > 0 {
		id = id[:i]
	}
	s := strings.TrimSpace(strings.ReplaceAll(id, "-", " "))
	if s == "" {
		return id
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Newest returns the newest file time in dir, skipping what the backup
// skips. Zero when there's nothing.
func Newest(dir string, exclude []string) time.Time {
	m := LoadMatcher(dir, exclude...)
	var newest time.Time
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		if m.Ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if n++; n > 20000 {
			return filepath.SkipAll
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}
