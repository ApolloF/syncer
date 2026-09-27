// Package update looks for a newer Syncer release on GitHub, and replaces
// the running Syncer.exe with the release's.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReleasesURL is the only place a release link may point into.
const ReleasesURL = "https://github.com/ApolloF/syncer/releases/"

const (
	assetName = "Syncer.exe"
	maxSize   = 200 << 20
)

// Variables for tests.
var (
	latestURL = "https://api.github.com/repos/ApolloF/syncer/releases/latest"
	// downloadURL is the only place the new exe may come from.
	downloadURL = ReleasesURL + "download/"
)

// Release is a published Syncer release.
type Release struct {
	Tag string // e.g. "v0.7.0"
	URL string // release page
	Exe *Asset // the release's Syncer.exe; nil when it has none (or no checksum)
}

// Asset is a file attached to a release.
type Asset struct {
	URL    string
	Size   int64
	SHA256 string // hex, from GitHub's digest of the upload
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Syncer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, errors.New("GitHub answered " + resp.Status)
	}
	var r struct {
		Tag     string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"` // "sha256:<hex>"
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, err
	}
	if _, ok := parse(r.Tag); !ok {
		return Release{}, errors.New("unexpected release tag " + strconv.Quote(r.Tag))
	}
	url := r.HTMLURL
	if !strings.HasPrefix(url, ReleasesURL) {
		url = ReleasesURL + "latest"
	}
	rel := Release{Tag: r.Tag, URL: url}
	for _, a := range r.Assets {
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if a.Name != assetName || !strings.HasPrefix(a.URL, downloadURL) || a.Size <= 0 || a.Size > maxSize || !ok {
			continue
		}
		if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
			continue
		}
		rel.Exe = &Asset{URL: a.URL, Size: a.Size, SHA256: strings.ToLower(sum)}
	}
	return rel, nil
}

// Download fetches a into dir (next to the exe it replaces, so the swap is a
// rename on one volume) and checks it against its checksum. It returns the
// downloaded file.
func Download(ctx context.Context, a *Asset, dir string) (string, error) {
	if a == nil || !strings.HasPrefix(a.URL, downloadURL) {
		return "", errors.New("the release has no Syncer.exe to update from")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Syncer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("GitHub answered " + resp.Status)
	}
	path := filepath.Join(dir, newName)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, a.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != a.Size {
		err = errors.New("the download is incomplete")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		err = errors.New("the download doesn't match the release's checksum")
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// Names of the new exe while it downloads, and of the replaced one until it
// can be deleted (Windows lets a running exe be renamed, not deleted).
const (
	newName = "Syncer.exe.new"
	oldName = "Syncer.exe.old"
)

// Replace puts the downloaded file in place of exe. The running exe is moved
// aside first; Cleanup deletes it once no process uses it any more.
func Replace(exe, downloaded string) error {
	old := filepath.Join(filepath.Dir(exe), oldName)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Still running from an earlier update (a background task, say):
		// move it out of the way under another name.
		if err := os.Rename(old, old+strconv.FormatInt(time.Now().UnixNano(), 36)); err != nil {
			return err
		}
	}
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(downloaded, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}
	return nil
}

// Cleanup deletes what earlier updates left next to exe. Files still in use
// stay until a later start.
func Cleanup(exe string) {
	dir := filepath.Dir(exe)
	ms, _ := filepath.Glob(filepath.Join(dir, oldName+"*"))
	for _, m := range append(ms, filepath.Join(dir, newName)) {
		_ = os.Remove(m)
	}
}

// Newer reports whether version tag a is newer than b ("v1.2.3", "1.2").
// Anything that isn't a version (like "dev") is never newer nor older.
func Newer(a, b string) bool {
	va, oka := parse(a)
	vb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

// Valid reports whether v is a release version (not e.g. "dev").
func Valid(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i] // pre-release/build suffix
	}
	parts := strings.Split(v, ".")
	if v == "" || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
