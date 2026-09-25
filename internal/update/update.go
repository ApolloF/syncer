// Package update looks for a newer Syncer release on GitHub.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	latestURL = "https://api.github.com/repos/ApolloF/syncer/releases/latest"
	// ReleasesURL is the only place a release link may point into.
	ReleasesURL = "https://github.com/ApolloF/syncer/releases/"
)

// Release is a published Syncer release.
type Release struct {
	Tag string // e.g. "v0.7.0"
	URL string // release page
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
	return Release{Tag: r.Tag, URL: url}, nil
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
