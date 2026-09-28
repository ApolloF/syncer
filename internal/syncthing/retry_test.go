package syncthing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const saveFailed = `rename C:\Users\x\AppData\Local\Syncthing\.syncthing.tmp.123 \?\C:\Users\x\AppData\Local\Syncthing\config.xml: Access is denied.`

func fastRetries(t *testing.T) {
	old := configRetries
	configRetries = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { configRetries = old })
}

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewAt(strings.TrimPrefix(srv.URL, "http://"), "key")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfigSaveRetried(t *testing.T) {
	fastRetries(t)
	var n atomic.Int32
	var bodies []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if n.Add(1) < 3 {
			http.Error(w, saveFailed, http.StatusInternalServerError)
		}
	})
	if err := c.PatchFolder(context.Background(), "f", map[string]any{"paused": true}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if n.Load() != 3 {
		t.Fatalf("tries = %d, want 3", n.Load())
	}
	for _, b := range bodies {
		if b != `{"paused":true}` {
			t.Fatalf("body resent as %q", b)
		}
	}
}

func TestConfigSaveGivesUp(t *testing.T) {
	fastRetries(t)
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, saveFailed, http.StatusInternalServerError)
	})
	err := c.PatchFolder(context.Background(), "f", map[string]any{"paused": true})
	if !isStatus(err, http.StatusInternalServerError) {
		t.Fatalf("err = %v", err)
	}
	if int(n.Load()) != len(configRetries)+1 {
		t.Fatalf("tries = %d", n.Load())
	}
}

func TestOtherErrorsNotRetried(t *testing.T) {
	fastRetries(t)
	for _, tc := range []struct {
		name, method string
		code         int
		msg          string
	}{
		{"bad request", http.MethodPatch, http.StatusBadRequest, "config.xml bad"},
		{"other 500", http.MethodPatch, http.StatusInternalServerError, "something else"},
		{"get", http.MethodGet, http.StatusInternalServerError, saveFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				http.Error(w, tc.msg, tc.code)
			})
			if err := c.do(context.Background(), tc.method, "/rest/config/folders/f", nil, nil); err == nil {
				t.Fatal("want error")
			}
			if n.Load() != 1 {
				t.Fatalf("tries = %d, want 1", n.Load())
			}
		})
	}
}

func TestDeleteRetryFindsItGone(t *testing.T) {
	fastRetries(t)
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, saveFailed, http.StatusInternalServerError)
			return
		}
		http.Error(w, "No folder with given ID", http.StatusNotFound)
	})
	if err := c.RemoveFolder(context.Background(), "f"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// A 404 on the first try is still an error.
	n.Store(1)
	if err := c.RemoveFolder(context.Background(), "f"); !isStatus(err, http.StatusNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	old := configRetries
	configRetries = []time.Duration{time.Hour}
	t.Cleanup(func() { configRetries = old })
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, saveFailed, http.StatusInternalServerError)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.PatchFolder(ctx, "f", map[string]any{"paused": true})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestReadConfigCached(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.xml")
	write := func(key string, mod time.Time) {
		if err := os.WriteFile(p, []byte(`<configuration><gui><address>127.0.0.1:8384</address><apikey>`+key+`</apikey></gui></configuration>`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	write("aaaa", t0)
	c, err := readConfig(p)
	if err != nil || c.GUI.APIKey != "aaaa" {
		t.Fatalf("%v %q", err, c.GUI.APIKey)
	}
	// A changed file is read again.
	write("bbbbbb", t0.Add(time.Second))
	if c, _ = readConfig(p); c.GUI.APIKey != "bbbbbb" {
		t.Fatalf("stale key %q", c.GUI.APIKey)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(p); err == nil {
		t.Fatal("missing config should fail")
	}
}
