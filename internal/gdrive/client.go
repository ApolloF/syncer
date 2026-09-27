package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Drive's REST endpoints; variables so tests can point them at a fake.
var (
	apiBase    = "https://www.googleapis.com/drive/v3"
	uploadBase = "https://www.googleapis.com/upload/drive/v3"
)

const (
	folderType   = "application/vnd.google-apps.folder"
	fileFields   = "id,name,mimeType,parents,size,md5Checksum,modifiedTime,createdTime"
	multipartMax = 5 << 20 // bigger files go up as a resumable upload
	minGap       = 100 * time.Millisecond
	maxTries     = 6
)

// File is a file or folder in Drive.
type File struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	MimeType string    `json:"mimeType"`
	Parents  []string  `json:"parents"`
	Size     int64     `json:"size,string"`
	MD5      string    `json:"md5Checksum"`
	Modified time.Time `json:"modifiedTime"`
	Created  time.Time `json:"createdTime"`
}

// Folder reports whether f is a folder.
func (f File) Folder() bool { return f.MimeType == folderType }

// Client calls the Drive API as the signed-in user. It refreshes the access
// token when needed (saving it with save), spaces requests out, and retries
// when Google asks to slow down or has a hiccup.
type Client struct {
	cfg  Config
	save func(Token) error

	mu   sync.Mutex
	tok  Token
	last time.Time
}

// NewClient returns a client for the signed-in user.
func NewClient(cfg Config, tok Token, save func(Token) error) *Client {
	return &Client{cfg: cfg, tok: tok, save: save}
}

func (c *Client) access(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if force {
		c.tok.Access = ""
	}
	t, err := c.tok.Fresh(ctx, c.cfg)
	if err != nil {
		return "", err
	}
	if t.Access != c.tok.Access {
		c.tok = t
		if c.save != nil {
			_ = c.save(t)
		}
	}
	return c.tok.Access, nil
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	d := time.Until(c.last.Add(minGap))
	c.last = time.Now().Add(max(d, 0))
	c.mu.Unlock()
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// apiError is an error answer from Drive.
type apiError struct {
	Status int
	Reason string
	Msg    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Google Drive: %s (%d)", cmpOr(e.Msg, e.Reason), e.Status)
}

// IsNotFound reports whether err is Drive saying a file doesn't exist.
func IsNotFound(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func retryable(e *apiError) bool {
	switch {
	case e.Status == http.StatusTooManyRequests || e.Status >= 500:
		return true
	case e.Status == http.StatusForbidden:
		return e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
	}
	return false
}

// do sends a request (built fresh for every try by mk) and decodes a JSON
// answer into out, or copies it to w.
func (c *Client) do(ctx context.Context, mk func() (*http.Request, error), out any, w io.Writer) (http.Header, error) {
	refreshed := false
	for try := 0; ; try++ {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		tok, err := c.access(ctx, false)
		if err != nil {
			return nil, err
		}
		req, err := mk()
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= maxTries-1 {
				return nil, err
			}
			backoff(ctx, try)
			continue
		}
		if resp.StatusCode < 300 {
			defer resp.Body.Close()
			switch {
			case w != nil:
				_, err = io.Copy(w, resp.Body)
			case out != nil:
				err = json.NewDecoder(resp.Body).Decode(out)
			default:
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			return resp.Header, err
		}
		e := readError(resp)
		if e.Status == http.StatusUnauthorized && !refreshed {
			refreshed = true
			if _, err := c.access(ctx, true); err != nil {
				return nil, err
			}
			continue
		}
		if !retryable(e) || try >= maxTries-1 {
			return nil, e
		}
		backoff(ctx, try)
	}
}

func readError(resp *http.Response) *apiError {
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	e := &apiError{Status: resp.StatusCode, Msg: body.Error.Message}
	if len(body.Error.Errors) > 0 {
		e.Reason = body.Error.Errors[0].Reason
	}
	return e
}

// backoff waits 1, 2, 4, … seconds (at most 32).
func backoff(ctx context.Context, try int) {
	d := time.Second << min(try, 5)
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func get(u string) func() (*http.Request, error) {
	return func() (*http.Request, error) { return http.NewRequest(http.MethodGet, u, nil) }
}

func jsonReq(method, u string, body any) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(method, u, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		}
		return req, err
	}
}

// Account returns the signed-in account's email address.
func (c *Client) Account(ctx context.Context) (string, error) {
	var about struct {
		User struct {
			Email string `json:"emailAddress"`
		} `json:"user"`
	}
	_, err := c.do(ctx, get(apiBase+"/about?fields=user(emailAddress)"), &about, nil)
	return about.User.Email, err
}

// List returns every file and folder Syncer can see (the ones it made), not
// trashed.
func (c *Client) List(ctx context.Context) ([]File, error) {
	var out []File
	page := ""
	for {
		v := url.Values{
			"q":        {"trashed=false"},
			"fields":   {"nextPageToken,files(" + fileFields + ")"},
			"pageSize": {"1000"},
			"spaces":   {"drive"},
		}
		if page != "" {
			v.Set("pageToken", page)
		}
		var res struct {
			Next  string `json:"nextPageToken"`
			Files []File `json:"files"`
		}
		if _, err := c.do(ctx, get(apiBase+"/files?"+v.Encode()), &res, nil); err != nil {
			return nil, err
		}
		out = append(out, res.Files...)
		if res.Next == "" {
			return out, nil
		}
		page = res.Next
	}
}

// FindTop returns the folders named name at the top of My Drive, oldest first.
func (c *Client) FindTop(ctx context.Context, name string) ([]File, error) {
	v := url.Values{
		"q":       {fmt.Sprintf("name = '%s' and mimeType = '%s' and 'root' in parents and trashed = false", escapeQ(name), folderType)},
		"fields":  {"files(" + fileFields + ")"},
		"orderBy": {"createdTime"},
		"spaces":  {"drive"},
	}
	var res struct {
		Files []File `json:"files"`
	}
	_, err := c.do(ctx, get(apiBase+"/files?"+v.Encode()), &res, nil)
	return res.Files, err
}

func escapeQ(s string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) }

// Mkdir creates a folder in parent ("root": the top of My Drive).
func (c *Client) Mkdir(ctx context.Context, parent, name string) (File, error) {
	var f File
	_, err := c.do(ctx, jsonReq(http.MethodPost, apiBase+"/files?fields="+fileFields,
		map[string]any{"name": name, "mimeType": folderType, "parents": []string{parent}}), &f, nil)
	return f, err
}

// Upload creates a file in parent from the local file at path, with its
// modification time.
func (c *Client) Upload(ctx context.Context, parent, name, path string, mtime time.Time) (File, error) {
	meta := map[string]any{"name": name, "parents": []string{parent}, "modifiedTime": mtime.UTC().Format(time.RFC3339Nano)}
	return c.send(ctx, http.MethodPost, uploadBase+"/files", meta, path)
}

// Update replaces a file's content with the local file at path.
func (c *Client) Update(ctx context.Context, id, path string, mtime time.Time) (File, error) {
	meta := map[string]any{"modifiedTime": mtime.UTC().Format(time.RFC3339Nano)}
	return c.send(ctx, http.MethodPatch, uploadBase+"/files/"+url.PathEscape(id), meta, path)
}

func (c *Client) send(ctx context.Context, method, u string, meta map[string]any, path string) (File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if fi.Size() <= multipartMax {
		_, err = c.do(ctx, func() (*http.Request, error) {
			body, ctype, err := multipartBody(meta, path)
			if err != nil {
				return nil, err
			}
			req, err := http.NewRequest(method, u+"?uploadType=multipart&fields="+fileFields, body)
			if err == nil {
				req.Header.Set("Content-Type", ctype)
			}
			return req, err
		}, &f, nil)
		return f, err
	}
	// Resumable: ask for an upload address, then send the whole file there.
	h, err := c.do(ctx, func() (*http.Request, error) {
		mk := jsonReq(method, u+"?uploadType=resumable&fields="+fileFields, meta)
		req, err := mk()
		if err == nil {
			req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(fi.Size(), 10))
		}
		return req, err
	}, nil, nil)
	if err != nil {
		return File{}, err
	}
	loc := h.Get("Location")
	if loc == "" {
		return File{}, errors.New("Google Drive gave no upload address")
	}
	_, err = c.do(ctx, func() (*http.Request, error) {
		in, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPut, loc, in)
		if err != nil {
			in.Close()
			return nil, err
		}
		req.ContentLength = fi.Size()
		return req, nil
	}, &f, nil)
	return f, err
}

// multipartBody builds a multipart/related body: JSON metadata, then the file.
func multipartBody(meta map[string]any, path string) (io.Reader, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mh := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
	pw, err := mw.CreatePart(mh)
	if err != nil {
		return nil, "", err
	}
	if err := json.NewEncoder(pw).Encode(meta); err != nil {
		return nil, "", err
	}
	pw, err = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/octet-stream"}})
	if err != nil {
		return nil, "", err
	}
	if _, err := pw.Write(data); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, "multipart/related; boundary=" + mw.Boundary(), nil
}

// Download writes a file's content to w.
func (c *Client) Download(ctx context.Context, id string, w io.Writer) error {
	_, err := c.do(ctx, get(apiBase+"/files/"+url.PathEscape(id)+"?alt=media"), nil, w)
	return err
}

// Move renames a file and moves it from one folder to another.
func (c *Client) Move(ctx context.Context, id, from, to, name string) (File, error) {
	v := url.Values{"fields": {fileFields}}
	if from != to {
		v.Set("addParents", to)
		v.Set("removeParents", from)
	}
	var f File
	_, err := c.do(ctx, jsonReq(http.MethodPatch, apiBase+"/files/"+url.PathEscape(id)+"?"+v.Encode(), map[string]any{"name": name}), &f, nil)
	return f, err
}

// Delete removes a file for good (Syncer only deletes what it made).
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequest(http.MethodDelete, apiBase+"/files/"+url.PathEscape(id), nil)
	}, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}
