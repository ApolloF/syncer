// Package discover finds game save folders on this PC using the community
// Ludusavi manifest (save locations sourced from PCGamingWiki).
package discover

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

const manifestURL = "https://raw.githubusercontent.com/mtkennerly/ludusavi-manifest/master/data/manifest.yaml"

const refreshAfter = 7 * 24 * time.Hour

// Entry is one game's Windows save locations, with manifest placeholders.
type Entry struct {
	Name        string
	Paths       []string
	SteamCloud  bool
	InstallDirs []string
	SteamID     int
}

var (
	cacheMu sync.Mutex
	cached  []Entry
)

func cacheDir() string {
	d := filepath.Join(paths.Root(paths.Local), "Syncer", "cache")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func indexFile() string { return filepath.Join(cacheDir(), "manifest-index-v3.gob.gz") }

// CachedManifest returns the local index without downloading or refreshing it.
func CachedManifest() []Entry {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cached == nil {
		if es, err := readIndex(); err == nil {
			cached = es
		}
	}
	return cached
}

// Manifest returns the parsed manifest index, downloading or refreshing it when
// needed. A stale cache is used if the network is unavailable.
func Manifest(forceRefresh bool) ([]Entry, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	fi, statErr := os.Stat(indexFile())
	fresh := statErr == nil && time.Since(fi.ModTime()) < refreshAfter
	if cached != nil && fresh && !forceRefresh {
		return cached, nil
	}
	if fresh && !forceRefresh {
		if es, err := readIndex(); err == nil {
			cached = es
			return es, nil
		}
	}
	es, err := download()
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		if old, rerr := readIndex(); rerr == nil {
			cached = old
			return old, nil
		}
		return nil, fmt.Errorf("could not download the game database: %w", err)
	}
	_ = writeIndex(es)
	cached = es
	return es, nil
}

// ManifestAge returns when the local index was last refreshed (zero if never).
func ManifestAge() time.Time {
	if fi, err := os.Stat(indexFile()); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func download() ([]Entry, error) {
	c := &http.Client{Timeout: 3 * time.Minute}
	resp, err := c.Get(manifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	es, err := Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(es) < 1000 {
		return nil, fmt.Errorf("game database looks incomplete (%d entries)", len(es))
	}
	return es, nil
}

func readIndex() ([]Entry, error) {
	f, err := os.Open(indexFile())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var es []Entry
	return es, gob.NewDecoder(zr).Decode(&es)
}

func writeIndex(es []Entry) error {
	tmp := indexFile() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if err := gob.NewEncoder(zw).Encode(es); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, indexFile()); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(cacheDir(), "manifest-index*.gob.gz"))
	for _, name := range old {
		if name != indexFile() {
			_ = os.Remove(name)
		}
	}
	return nil
}

// Parse reads the manifest YAML. The file is machine-generated with a fixed
// 2-space layout, so a line scanner is used instead of a generic YAML decoder:
// it is ~20x faster and needs a few MB instead of hundreds.
func Parse(r io.Reader) ([]Entry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var (
		out     []Entry
		cur     *Entry
		section string
		p       *pathInfo
	)
	flush := func() {
		if p != nil && cur != nil && p.relevant() {
			cur.Paths = append(cur.Paths, p.path)
		}
		p = nil
	}
	finish := func() {
		flush()
		if cur != nil && len(cur.Paths) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || line == "---" {
			continue
		}
		ind := len(line) - len(strings.TrimLeft(line, " "))
		t := strings.TrimSpace(line)
		switch {
		case ind == 0:
			finish()
			cur = &Entry{Name: unquote(strings.TrimSuffix(t, ":"))}
			section = ""
		case cur == nil:
		case ind == 2:
			flush()
			section = strings.TrimSuffix(t, ":")
		case section == "cloud" && ind == 4:
			if k, v, ok := strings.Cut(t, ":"); ok && strings.TrimSpace(k) == "steam" && strings.TrimSpace(v) == "true" {
				cur.SteamCloud = true
			}
		case section == "installDir" && ind == 4:
			dir := unquote(strings.TrimSuffix(strings.TrimSuffix(t, ": {}"), ":"))
			if dir != "" {
				cur.InstallDirs = append(cur.InstallDirs, dir)
			}
		case section == "steam" && ind == 4:
			if k, v, ok := strings.Cut(t, ":"); ok && k == "id" {
				if id, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && id > 0 {
					cur.SteamID = id
				}
			}
		case section == "files" && ind == 4:
			flush()
			p = &pathInfo{path: unquote(strings.TrimSuffix(t, ":"))}
		case section == "files" && p != nil && ind == 6:
			p.sub = strings.TrimSuffix(t, ":")
		case section == "files" && p != nil && ind >= 8:
			v := strings.TrimSpace(strings.TrimPrefix(t, "- "))
			switch p.sub {
			case "tags":
				p.tags = append(p.tags, v)
			case "when":
				if strings.HasPrefix(t, "- ") {
					p.whens++
				}
				if k, val, ok := strings.Cut(v, ":"); ok && strings.TrimSpace(k) == "os" {
					p.oses = append(p.oses, strings.TrimSpace(val))
				}
			}
		}
	}
	finish()
	return out, sc.Err()
}

type pathInfo struct {
	path  string
	sub   string
	tags  []string
	oses  []string
	whens int
}

var winPrefixes = []string{"<winAppData>", "<winLocalAppData>", "<winDocuments>", "<home>", "<winPublic>", "<winProgramData>"}

func (p *pathInfo) relevant() bool {
	ok := false
	for _, pre := range winPrefixes {
		if strings.HasPrefix(p.path, pre) {
			ok = true
			break
		}
	}
	if !ok {
		return false
	}
	if len(p.tags) > 0 {
		save := false
		for _, t := range p.tags {
			if t == "save" {
				save = true
			}
		}
		if !save {
			return false
		}
	}
	// Any os-restricted condition must allow Windows; store-only conditions
	// (no os) apply everywhere.
	if len(p.oses) > 0 && len(p.oses) >= p.whens {
		for _, o := range p.oses {
			if o == "windows" {
				return true
			}
		}
		return false
	}
	return true
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}
