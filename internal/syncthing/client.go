// Package syncthing talks to the local Syncthing instance over its REST API.
// The API key and address are read from Syncthing's own config.xml at runtime,
// so nothing secret is ever stored by Syncer.
package syncthing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// maxRespBody caps how much of a Syncthing response we ever decode, so a
// misbehaving or compromised instance can't exhaust memory.
const maxRespBody = 32 << 20

// ErrNotRunning means Syncthing's API did not answer.
var ErrNotRunning = errors.New("syncthing is not running")

// ErrUnauthorized means a Syncthing answered but rejected the API key from
// config.xml, i.e. the running instance uses a different config directory.
var ErrUnauthorized = errors.New(`Syncthing is running but rejected Syncer's key: it uses a different config than %LOCALAPPDATA%\Syncthing`)

// ConfigPath is Syncthing's default config location on Windows.
func ConfigPath() string {
	return filepath.Join(paths.Root(paths.Local), "Syncthing", "config.xml")
}

type xmlConfig struct {
	GUI struct {
		TLS     bool   `xml:"tls,attr"`
		Address string `xml:"address"`
		APIKey  string `xml:"apikey"`
	} `xml:"gui"`
}

// Client is a minimal Syncthing REST client.
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// cfgCache holds the last parsed config.xml. Syncer makes a client for
// nearly every call; reading config.xml each time kept a handle open on it
// often enough that Syncthing's own save (a rename over config.xml) failed
// with "Access is denied" on Windows. A stat takes no handle.
var cfgCache struct {
	sync.Mutex
	mod  time.Time
	size int64
	cfg  xmlConfig
	ok   bool
}

// readConfig returns config.xml's GUI settings, reading the file only when
// its size or modification time changed.
func readConfig(path string) (xmlConfig, error) {
	st, err := os.Stat(path)
	if err != nil {
		return xmlConfig{}, fmt.Errorf("syncthing config not found: %w", err)
	}
	cfgCache.Lock()
	defer cfgCache.Unlock()
	if cfgCache.ok && st.ModTime().Equal(cfgCache.mod) && st.Size() == cfgCache.size {
		return cfgCache.cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return xmlConfig{}, fmt.Errorf("syncthing config not found: %w", err)
	}
	var c xmlConfig
	if err := xml.Unmarshal(b, &c); err != nil {
		return xmlConfig{}, fmt.Errorf("parse syncthing config: %w", err)
	}
	cfgCache.mod, cfgCache.size, cfgCache.cfg, cfgCache.ok = st.ModTime(), st.Size(), c, true
	return c, nil
}

// New reads config.xml and returns a client. It does not contact Syncthing.
func New() (*Client, error) {
	c, err := readConfig(ConfigPath())
	if err != nil {
		return nil, err
	}
	if c.GUI.APIKey == "" {
		return nil, errors.New("syncthing config has no API key")
	}
	addr := normalizeHost(c.GUI.Address)
	loopback := isLoopbackHost(hostOnly(addr))
	if !c.GUI.TLS && !loopback {
		// Never send the API key in the clear to a non-local address.
		return nil, errors.New("Syncthing's web GUI listens on a network address without HTTPS; Syncer only connects to it on this PC")
	}
	transport, err := certPinnedTransport(c.GUI.TLS, loopback, filepath.Dir(ConfigPath()))
	if err != nil {
		return nil, err
	}
	scheme := "http"
	if c.GUI.TLS {
		scheme = "https"
	}
	return &Client{
		base:   scheme + "://" + addr,
		apiKey: c.GUI.APIKey,
		http:   &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}, nil
}

// PatchOptions changes Syncthing's global options.
func (c *Client) PatchOptions(ctx context.Context, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/options", patch, nil)
}

// NewAt returns a client for a Syncthing on this PC at a plain-HTTP
// loopback address (tests and tools; Syncer itself uses New).
func NewAt(addr, apiKey string) (*Client, error) {
	if !isLoopbackHost(hostOnly(addr)) {
		return nil, errors.New("only a Syncthing on this PC")
	}
	return &Client{base: "http://" + addr, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// normalizeHost rewrites an unspecified bind address ("", "0.0.0.0", "::",
// "[::]") to loopback, keeping the port, and fills in the default port.
// Syncthing's config may bind those to mean "listen on every interface";
// Syncer only ever wants to reach it on this PC.
func normalizeHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if port == "" {
		port = "8384"
	}
	return net.JoinHostPort(host, port)
}

// hostOnly strips the port from a host:port address.
func hostOnly(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// isLoopbackHost reports whether host only ever resolves to this PC.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// certPinnedTransport builds the transport used to reach Syncthing's GUI.
// With TLS, it pins Syncthing's own self-signed cert (read from
// https-cert.pem next to config.xml) instead of trusting any cert a server
// at that address presents. If the pem can't be read, a loopback host falls
// back to the old unverified-but-local behavior; a non-loopback host is
// refused, since there is nothing to authenticate it with.
func certPinnedTransport(useTLS, loopback bool, configDir string) (http.RoundTripper, error) {
	if !useTLS {
		return http.DefaultTransport, nil
	}
	pinned, err := readPinnedCert(configDir)
	if err != nil {
		if loopback {
			return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}, nil
		}
		return nil, errors.New("Syncthing's GUI certificate could not be read; refusing to connect to a network address")
	}
	return &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify:    true, // we verify the leaf ourselves, against the pinned cert
		MinVersion:            tls.VersionTLS12,
		VerifyPeerCertificate: verifyPinnedCert(pinned),
	}}, nil
}

// readPinnedCert returns the DER bytes of Syncthing's GUI certificate.
func readPinnedCert(configDir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(configDir, "https-cert.pem"))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("https-cert.pem has no PEM block")
	}
	return block.Bytes, nil
}

// verifyPinnedCert rejects any leaf certificate that isn't byte-identical to
// the pinned one, so a network attacker can't present their own cert.
func verifyPinnedCert(pinnedDER []byte) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], pinnedDER) {
			return errors.New("syncthing's GUI certificate does not match the pinned certificate")
		}
		return nil
	}
}

// GUIURL is the address of Syncthing's own web UI.
func (c *Client) GUIURL() string { return c.base }

// StatusError is an error status Syncthing answered with.
type StatusError struct {
	Code int
	msg  string
}

func (e *StatusError) Error() string { return e.msg }

// configRetries is how long a config change waits between tries when
// Syncthing could not save config.xml (tests shorten it).
var configRetries = []time.Duration{300 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for i := 0; ; i++ {
		err := c.once(ctx, method, path, b, body != nil, out)
		if i >= len(configRetries) || !configSaveFailed(method, path, err) {
			if i > 0 && method == http.MethodDelete && isStatus(err, http.StatusNotFound) {
				// The first try removed it from the running config before
				// the save failed; the retry that saved it finds it gone.
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(configRetries[i]):
		}
	}
}

// configSaveFailed reports a config change Syncthing applied but could not
// write to config.xml, typically "Access is denied" on the rename over it
// while another program (antivirus, a backup, an indexer) had it open.
// Syncthing keeps the change in memory, so sending it again is safe: the
// same folder or device is set to the same values, then saved.
func configSaveFailed(method, path string, err error) bool {
	var se *StatusError
	if method == http.MethodGet || !strings.HasPrefix(path, "/rest/config") ||
		!errors.As(err, &se) || se.Code != http.StatusInternalServerError {
		return false
	}
	return strings.Contains(strings.ToLower(se.msg), "config.xml")
}

func isStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

func (c *Client) once(ctx context.Context, method, path string, body []byte, hasBody bool, out any) error {
	var rd io.Reader
	if hasBody {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		cfgCache.Lock()
		cfgCache.ok = false // the key may have changed; read config.xml again next time
		cfgCache.Unlock()
		return ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode,
			msg: fmt.Sprintf("syncthing %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxRespBody)).Decode(out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// ---- types -----------------------------------------------------------------

type FolderDevice struct {
	DeviceID     string `json:"deviceID"`
	IntroducedBy string `json:"introducedBy,omitempty"`
}

type Versioning struct {
	Type             string            `json:"type"`
	Params           map[string]string `json:"params"`
	CleanupIntervalS int               `json:"cleanupIntervalS"`
	FSPath           string            `json:"fsPath"`
	FSType           string            `json:"fsType"`
}

type Folder struct {
	ID               string         `json:"id"`
	Label            string         `json:"label"`
	Path             string         `json:"path"`
	Type             string         `json:"type"`
	Devices          []FolderDevice `json:"devices"`
	Paused           bool           `json:"paused"`
	FSWatcherEnabled bool           `json:"fsWatcherEnabled"`
	RescanIntervalS  int            `json:"rescanIntervalS"`
	IgnorePerms      bool           `json:"ignorePerms"`
	Versioning       Versioning     `json:"versioning"`
	// MaxConflicts is how many conflict copies of a file Syncthing keeps
	// (-1: all of them, 0: none, the default is 10).
	MaxConflicts int `json:"maxConflicts"`
}

type Device struct {
	DeviceID          string   `json:"deviceID"`
	Name              string   `json:"name"`
	Addresses         []string `json:"addresses"`
	Introducer        bool     `json:"introducer"`
	AutoAcceptFolders bool     `json:"autoAcceptFolders"`
	Paused            bool     `json:"paused"`
}

type SystemStatus struct {
	MyID   string `json:"myID"`
	Uptime int    `json:"uptime"`
}

type Connection struct {
	Connected     bool   `json:"connected"`
	Address       string `json:"address"`
	ClientVersion string `json:"clientVersion"`
	Type          string `json:"type"`    // e.g. tcp-client, quic-server, relay-client
	IsLocal       bool   `json:"isLocal"` // on the local network
}

// Via says how a connection reaches the other PC: "lan", "relay" (through a
// public Syncthing relay, end-to-end encrypted) or "direct" (over the
// internet).
func (cn Connection) Via() string {
	switch {
	case strings.HasPrefix(cn.Type, "relay"):
		return "relay"
	case cn.IsLocal:
		return "lan"
	}
	return "direct"
}

type Connections struct {
	Connections map[string]Connection `json:"connections"`
}

type FolderStatus struct {
	State       string `json:"state"`
	GlobalBytes int64  `json:"globalBytes"`
	GlobalFiles int    `json:"globalFiles"`
	LocalBytes  int64  `json:"localBytes"`
	NeedBytes   int64  `json:"needBytes"`
	NeedFiles   int    `json:"needFiles"`
	Errors      int    `json:"errors"`
	PullErrors  int    `json:"pullErrors"`
	// ReceiveOnlyTotalItems are files changed locally in a receive-only
	// folder (they'd be put back by Revert).
	ReceiveOnlyTotalItems int `json:"receiveOnlyTotalItems"`
}

type Completion struct {
	Completion float64 `json:"completion"`
	NeedBytes  int64   `json:"needBytes"`
}

type PendingDevice struct {
	Name    string    `json:"name"`
	Address string    `json:"address"`
	Time    time.Time `json:"time"`
}

// PendingFolders maps folder id -> offeredBy device id -> offer.
type PendingFolders map[string]struct {
	OfferedBy map[string]struct {
		Label string    `json:"label"`
		Time  time.Time `json:"time"`
	} `json:"offeredBy"`
}

type Event struct {
	ID   int             `json:"id"`
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

// ---- calls -----------------------------------------------------------------

func (c *Client) Status(ctx context.Context) (SystemStatus, error) {
	var s SystemStatus
	return s, c.get(ctx, "/rest/system/status", &s)
}

func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	var f []Folder
	return f, c.get(ctx, "/rest/config/folders", &f)
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var d []Device
	return d, c.get(ctx, "/rest/config/devices", &d)
}

func (c *Client) Connections(ctx context.Context) (Connections, error) {
	var cs Connections
	return cs, c.get(ctx, "/rest/system/connections", &cs)
}

func (c *Client) FolderStatus(ctx context.Context, id string) (FolderStatus, error) {
	var s FolderStatus
	return s, c.get(ctx, "/rest/db/status?folder="+url.QueryEscape(id), &s)
}

// Completion of all folders shared with device.
func (c *Client) Completion(ctx context.Context, device string) (Completion, error) {
	var s Completion
	return s, c.get(ctx, "/rest/db/completion?device="+url.QueryEscape(device), &s)
}

func (c *Client) PendingDevices(ctx context.Context) (map[string]PendingDevice, error) {
	m := map[string]PendingDevice{}
	return m, c.get(ctx, "/rest/cluster/pending/devices", &m)
}

func (c *Client) PendingFolders(ctx context.Context) (PendingFolders, error) {
	m := PendingFolders{}
	return m, c.get(ctx, "/rest/cluster/pending/folders", &m)
}

// AddFolder creates a folder; Syncthing fills unset fields from its defaults.
func (c *Client) AddFolder(ctx context.Context, f map[string]any) error {
	return c.do(ctx, http.MethodPost, "/rest/config/folders", f, nil)
}

// PatchFolder merges fields into an existing folder config.
func (c *Client) PatchFolder(ctx context.Context, id string, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/folders/"+url.PathEscape(id), patch, nil)
}

func (c *Client) RemoveFolder(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/rest/config/folders/"+url.PathEscape(id), nil, nil)
}

// FileInfo is what Syncthing knows about one file.
type FileInfo struct {
	Modified   time.Time `json:"modified"`
	ModifiedBy string    `json:"modifiedBy"` // short id of the device that last changed it
	Deleted    bool      `json:"deleted"`
	Size       int64     `json:"size"`
}

// DBFile returns the newest known version of a file (rel uses forward
// slashes, relative to the folder).
func (c *Client) DBFile(ctx context.Context, folder, rel string) (FileInfo, error) {
	var r struct {
		Global FileInfo `json:"global"`
	}
	return r.Global, c.get(ctx, "/rest/db/file?folder="+url.QueryEscape(folder)+"&file="+url.QueryEscape(rel), &r)
}

// Ignores returns the lines of a folder's .stignore, comments included.
func (c *Client) Ignores(ctx context.Context, id string) ([]string, error) {
	var r struct {
		Ignore []string `json:"ignore"`
	}
	return r.Ignore, c.get(ctx, "/rest/db/ignores?folder="+url.QueryEscape(id), &r)
}

// SetIgnores replaces a folder's .stignore with lines.
func (c *Client) SetIgnores(ctx context.Context, id string, lines []string) error {
	if lines == nil {
		lines = []string{}
	}
	return c.do(ctx, http.MethodPost, "/rest/db/ignores?folder="+url.QueryEscape(id), map[string]any{"ignore": lines}, nil)
}

func (c *Client) AddDevice(ctx context.Context, d Device) error {
	return c.do(ctx, http.MethodPost, "/rest/config/devices", d, nil)
}

func (c *Client) RemoveDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), nil, nil)
}

// DismissPendingDevice removes an incoming connection request.
func (c *Client) DismissPendingDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/rest/cluster/pending/devices?device="+url.QueryEscape(id), nil, nil)
}

// Shutdown asks Syncthing to exit cleanly.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/rest/system/shutdown", nil, nil)
}

// Rescan asks Syncthing to rescan one folder (or all when id is "").
func (c *Client) Rescan(ctx context.Context, id string) error {
	p := "/rest/db/scan"
	if id != "" {
		p += "?folder=" + url.QueryEscape(id)
	}
	return c.do(ctx, http.MethodPost, p, nil, nil)
}

// ModifiedBy returns the short id of the device that last changed a file
// in a folder, as Syncthing's global index has it.
func (c *Client) ModifiedBy(ctx context.Context, folder, file string) (string, error) {
	var fi struct {
		Global struct {
			ModifiedBy string `json:"modifiedBy"`
		} `json:"global"`
	}
	err := c.get(ctx, "/rest/db/file?folder="+url.QueryEscape(folder)+"&file="+url.QueryEscape(file), &fi)
	return fi.Global.ModifiedBy, err
}

// GlobalFile is a file as the other devices have it (Syncthing's global
// index), whether or not it is ignored here.
type GlobalFile struct {
	Exists     bool // known at all
	Deleted    bool
	Size       int64
	ModifiedBy string // short device id
}

// Global looks a file up in a folder's global index.
func (c *Client) Global(ctx context.Context, folder, file string) (GlobalFile, error) {
	var fi struct {
		Global struct {
			Deleted    bool   `json:"deleted"`
			Size       int64  `json:"size"`
			ModifiedBy string `json:"modifiedBy"`
			Name       string `json:"name"`
		} `json:"global"`
	}
	err := c.get(ctx, "/rest/db/file?folder="+url.QueryEscape(folder)+"&file="+url.QueryEscape(file), &fi)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return GlobalFile{}, nil
	}
	if err != nil {
		return GlobalFile{}, err
	}
	return GlobalFile{Exists: fi.Global.Name != "", Deleted: fi.Global.Deleted, Size: fi.Global.Size, ModifiedBy: fi.Global.ModifiedBy}, nil
}

// LocalChanged lists the files changed here in a receive-only folder (what
// Revert would undo).
func (c *Client) LocalChanged(ctx context.Context, folder string) ([]string, error) {
	var out struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := c.get(ctx, "/rest/db/localchanged?folder="+url.QueryEscape(folder)+"&perpage=100000", &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Files))
	for _, f := range out.Files {
		names = append(names, f.Name)
	}
	return names, nil
}

// Revert undoes local changes in a receive-only folder: files changed or
// added here are replaced by (or deleted in favour of) the other devices'.
func (c *Client) Revert(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/rest/db/revert?folder="+url.QueryEscape(id), nil, nil)
}

// Events long-polls for events after since. Blocks up to ~60s.
func (c *Client) Events(ctx context.Context, since int, types string) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/rest/events?since=%d&timeout=50&events=%s", c.base, since, url.QueryEscape(types)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	hc := *c.http
	hc.Timeout = 70 * time.Second
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("syncthing events: %s", resp.Status)
	}
	var ev []Event
	return ev, json.NewDecoder(io.LimitReader(resp.Body, maxRespBody)).Decode(&ev)
}

// StaggeredVersioning keeps old copies of files replaced by other devices, so
// a corrupt save arriving from another PC never destroys the good one.
func StaggeredVersioning() map[string]any {
	return map[string]any{
		"type":             "staggered",
		"params":           map[string]string{"maxAge": "2592000", "cleanInterval": "3600"},
		"cleanupIntervalS": 3600,
		"fsPath":           "",
		"fsType":           "basic",
	}
}
