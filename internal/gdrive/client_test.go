package gdrive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGoogle serves the few endpoints the client uses.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	access    string // the access token that works
	refreshes int
	throttle  int               // answer this many requests with 429 first
	codes     map[string]string // code -> PKCE challenge
	uploads   map[string][]byte // name -> content
	meta      map[string]map[string]any
	calls     []string // "METHOD path body" of requests on single files
}

func newGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{t: t, access: "fresh", codes: map[string]string{}, uploads: map[string][]byte{}, meta: map[string]map[string]any{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	oldAPI, oldUp, oldTok, oldAuth := apiBase, uploadBase, tokenURL, authURL
	apiBase, uploadBase, tokenURL, authURL = g.srv.URL+"/drive/v3", g.srv.URL+"/upload/drive/v3", g.srv.URL+"/token", g.srv.URL+"/auth"
	t.Cleanup(func() { apiBase, uploadBase, tokenURL, authURL = oldAPI, oldUp, oldTok, oldAuth })
	return g
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			if r.Form.Get("refresh_token") != "good-refresh" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			g.refreshes++
		case "authorization_code":
			ch := g.codes[r.Form.Get("code")]
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if ch == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
		}
		_, _ = io.WriteString(w, `{"access_token":"fresh","refresh_token":"good-refresh","expires_in":3600}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+g.access {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid Credentials"}}`)
		return
	}
	if g.throttle > 0 {
		g.throttle--
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`)
		return
	}
	switch {
	case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet:
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"nextPageToken":"p2","files":[{"id":"1","name":"a","size":"3","modifiedTime":"2026-09-01T10:00:00.000Z"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"files":[{"id":"2","name":"b","mimeType":"application/vnd.google-apps.folder"}]}`)
		}
	case r.URL.Path == "/upload/drive/v3/files" && r.URL.Query().Get("uploadType") == "multipart":
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		p, _ := mr.NextPart()
		var meta map[string]any
		_ = json.NewDecoder(p).Decode(&meta)
		p, _ = mr.NextPart()
		b, _ := io.ReadAll(p)
		name := meta["name"].(string)
		g.uploads[name], g.meta[name] = b, meta
		_, _ = io.WriteString(w, `{"id":"new","name":"`+name+`"}`)
	case r.URL.Path == "/upload/drive/v3/files" && r.URL.Query().Get("uploadType") == "resumable":
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		g.meta[meta["name"].(string)] = meta
		w.Header().Set("Location", g.srv.URL+"/session?name="+url.QueryEscape(meta["name"].(string)))
	case r.URL.Path == "/session" && r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		g.uploads[r.URL.Query().Get("name")] = b
		_, _ = io.WriteString(w, `{"id":"big","name":"big"}`)
	case strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && (r.Method == http.MethodDelete || r.Method == http.MethodPatch):
		b, _ := io.ReadAll(r.Body)
		g.calls = append(g.calls, r.Method+" "+r.URL.Path+" "+string(b))
		_, _ = io.WriteString(w, `{"id":"x"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
	}
}

func TestClientRefreshesAndRetries(t *testing.T) {
	g := newGoogle(t)
	g.throttle = 1
	var saved Token
	c := NewClient(Config{ClientID: "id"}, Token{Refresh: "good-refresh", Access: "stale", Expiry: time.Now().Add(time.Hour)}, func(t Token) error { saved = t; return nil })
	fs, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Size != 3 || !fs[1].Folder() {
		t.Errorf("files: %+v", fs)
	}
	if g.refreshes != 1 || saved.Access != "fresh" || saved.Refresh != "good-refresh" {
		t.Errorf("refreshes=%d saved=%+v", g.refreshes, saved)
	}
}

func TestClientSignedOut(t *testing.T) {
	newGoogle(t)
	c := NewClient(Config{ClientID: "id"}, Token{Refresh: "revoked"}, nil)
	if _, err := c.List(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Errorf("err = %v, want ErrSignedOut", err)
	}
}

func TestClientUpload(t *testing.T) {
	g := newGoogle(t)
	c := NewClient(Config{ClientID: "id"}, Token{Refresh: "good-refresh", Access: "fresh", Expiry: time.Now().Add(time.Hour)}, nil)
	d := t.TempDir()
	small := filepath.Join(d, "small.sav")
	if err := os.WriteFile(small, []byte("save"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if _, err := c.Upload(context.Background(), "parent", "small.sav", small, at); err != nil {
		t.Fatal(err)
	}
	if string(g.uploads["small.sav"]) != "save" || g.meta["small.sav"]["modifiedTime"] != "2026-09-01T10:00:00Z" {
		t.Errorf("multipart upload: %q %v", g.uploads["small.sav"], g.meta["small.sav"])
	}
	big := filepath.Join(d, "big.sav")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", multipartMax+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload(context.Background(), "parent", "big.sav", big, at); err != nil {
		t.Fatal(err)
	}
	if len(g.uploads["big.sav"]) != multipartMax+1 {
		t.Errorf("resumable upload: %d bytes", len(g.uploads["big.sav"]))
	}
}

func TestSignIn(t *testing.T) {
	g := newGoogle(t)
	open := func(u string) {
		pu, err := url.Parse(u)
		if err != nil {
			t.Error(err)
			return
		}
		q := pu.Query()
		if q.Get("scope") != Scope || q.Get("code_challenge_method") != "S256" {
			t.Errorf("auth url: %s", u)
		}
		g.mu.Lock()
		g.codes["the-code"] = q.Get("code_challenge")
		g.mu.Unlock()
		go func() {
			// A request with the wrong state is refused and doesn't end sign-in.
			if resp, err := http.Get(q.Get("redirect_uri") + "?code=evil&state=wrong"); err == nil {
				resp.Body.Close()
			}
			resp, err := http.Get(q.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := SignIn(ctx, Config{ClientID: "id"}, open)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Refresh != "good-refresh" || tok.Access != "fresh" {
		t.Errorf("token: %+v", tok)
	}
}

func TestTokenStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gdrive.token")
	old := tokenFile
	tokenFile = func() string { return p }
	defer func() { tokenFile = old }()
	if _, err := LoadToken(); !errors.Is(err, ErrSignedOut) {
		t.Errorf("no token: %v", err)
	}
	want := Token{Refresh: "r", Access: "a", Account: "me@example.com"}
	if err := SaveToken(want); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "me@example.com") {
		t.Error("token stored in the clear")
	}
	got, err := LoadToken()
	if err != nil || got.Refresh != "r" || got.Account != want.Account {
		t.Errorf("round trip: %+v %v", got, err)
	}
	_ = os.WriteFile(p, []byte("garbage"), 0o600)
	if _, err := LoadToken(); !errors.Is(err, ErrSignedOut) {
		t.Errorf("garbage: %v", err)
	}
	if err := DeleteToken(); err != nil || SignedIn() {
		t.Errorf("delete: %v", err)
	}
}

// Deleting in Drive puts the file in the trash, where it can be brought
// back, instead of deleting it for good.
func TestClientDeleteTrashes(t *testing.T) {
	g := newGoogle(t)
	c := NewClient(Config{ClientID: "id"}, Token{Refresh: "good-refresh", Access: "fresh", Expiry: time.Now().Add(time.Hour)}, nil)
	if err := c.Delete(context.Background(), "f1"); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 1 || g.calls[0] != `PATCH /drive/v3/files/f1 {"trashed":true}` {
		t.Errorf("requests: %q, want one PATCH trashing f1", g.calls)
	}
}
