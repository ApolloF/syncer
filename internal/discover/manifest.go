// Package discover finds game save folders on this PC using the community
// Ludusavi manifest (save locations sourced from PCGamingWiki).
package discover

import (
	"compress/gzip"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ApolloF/gamekit/ludusavi"
	"github.com/ApolloF/syncer/internal/paths"
)

const manifestURL = ludusavi.URL

const refreshAfter = 7 * 24 * time.Hour

// Entry is one game's Windows save locations, with manifest placeholders.
type Entry struct {
	Name        string
	Paths       []string
	SteamCloud  bool     // the game supports Steam Cloud (not that it's in use here)
	SteamID     int      // Steam app id, 0 if not on Steam
	InstallDirs []string // folder names the game installs into
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

// Parse reads the manifest YAML and keeps the games that have Windows save
// locations.
func Parse(r io.Reader) ([]Entry, error) {
	es, err := ludusavi.Parse(r)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range es {
		if len(e.Saves) > 0 {
			out = append(out, Entry{Name: e.Name, Paths: e.Saves, SteamCloud: e.SteamCloud, SteamID: e.SteamID, InstallDirs: e.InstallDirs})
		}
	}
	return out, nil
}
