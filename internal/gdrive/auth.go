// Package gdrive backs up to Google Drive without Google Drive for desktop:
// Syncer signs in to the user's Google account itself (OAuth, drive.file
// scope: it only sees the files it made) and keeps a folder on this PC in
// sync with a GameSaveBackup folder in My Drive, the way Drive for desktop's
// mirror mode would.
package gdrive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config is the OAuth client Syncer is registered as (a "Desktop app" client
// in Google Cloud; its secret isn't secret for installed apps).
type Config struct {
	ClientID     string
	ClientSecret string
}

// Configured reports whether this build can sign in to Google.
func (c Config) Configured() bool { return c.ClientID != "" }

// Scope: only the files Syncer creates. It needs no security review by
// Google, and Syncer can't see anything else in the user's Drive.
const Scope = "https://www.googleapis.com/auth/drive.file"

// Google's endpoints; variables so tests can point them at a fake.
var (
	authURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL  = "https://oauth2.googleapis.com/token"
	revokeURL = "https://oauth2.googleapis.com/revoke"
)

// Token is what signing in gives: a refresh token that lasts, and an access
// token that lasts an hour.
type Token struct {
	Refresh string    `json:"refresh"`
	Access  string    `json:"access"`
	Expiry  time.Time `json:"expiry"`
	Account string    `json:"account"` // the Google account's email address
}

// SignIn opens Google's sign-in page in the browser (open) and waits for the
// user to allow access, which Google reports to a one-off listener on
// 127.0.0.1. It uses PKCE, so a code caught by another program is useless.
func SignIn(ctx context.Context, cfg Config, open func(string)) (Token, error) {
	if !cfg.Configured() {
		return Token{}, errors.New("this build of Syncer can't sign in to Google")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, err
	}
	defer ln.Close()
	redirect := "http://" + ln.Addr().String() + "/"
	verifier, challenge := pkce()
	state := randomString(24)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		res := result{code: q.Get("code")}
		if e := q.Get("error"); e != "" || res.code == "" {
			res.err = fmt.Errorf("Google sign-in didn't finish (%s)", cmpOr(e, "no code"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "Signed in. You can close this tab and go back to Syncer."
		if res.err != nil {
			msg = res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><title>Syncer</title><body style=\"font-family:sans-serif;margin:3em\"><p>%s</p></body>", html.EscapeString(msg))
		once.Do(func() { done <- res })
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	v := url.Values{
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {Scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"}, // always hand out a refresh token
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	open(authURL + "?" + v.Encode())

	var res result
	select {
	case <-ctx.Done():
		return Token{}, errors.New("Google sign-in timed out or was cancelled")
	case res = <-done:
	}
	if res.err != nil {
		return Token{}, res.err
	}
	return exchange(ctx, cfg, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
}

// Fresh returns t with a new access token when the current one is (nearly)
// expired.
func (t Token) Fresh(ctx context.Context, cfg Config) (Token, error) {
	if t.Access != "" && time.Until(t.Expiry) > time.Minute {
		return t, nil
	}
	if t.Refresh == "" {
		return t, ErrSignedOut
	}
	nt, err := exchange(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		return t, err
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh // Google keeps the old one valid
	}
	nt.Account = t.Account
	return nt, nil
}

// ErrSignedOut means the user has to sign in (again): never signed in, or
// Google no longer accepts the refresh token (revoked, password changed).
var ErrSignedOut = errors.New("signed out of Google: sign in again under Backup")

func exchange(ctx context.Context, cfg Config, v url.Values) (Token, error) {
	v.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		v.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Token{}, fmt.Errorf("Google sign-in: %s", resp.Status)
	}
	if body.Error == "invalid_grant" {
		return Token{}, ErrSignedOut
	}
	if resp.StatusCode != http.StatusOK || body.Access == "" {
		return Token{}, fmt.Errorf("Google sign-in: %s", cmpOr(body.Desc, body.Error, resp.Status))
	}
	return Token{Access: body.Access, Refresh: body.Refresh, Expiry: time.Now().Add(time.Duration(body.Expires) * time.Second)}, nil
}

// Revoke tells Google to forget Syncer's access (signing out).
func Revoke(ctx context.Context, t Token) error {
	tok := cmpOr(t.Refresh, t.Access)
	if tok == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(url.Values{"token": {tok}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	// 400 means Google no longer knows the token (already revoked or expired):
	// the access is gone either way.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("revoking Google access: %s", resp.Status)
	}
	return nil
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

// pkce returns a PKCE verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
