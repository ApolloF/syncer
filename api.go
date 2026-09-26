package main

// The launcher API: a local JSON-RPC 2.0 service on the named pipe
// \\.\pipe\syncer, for game launchers such as WaterLauncher. Only the
// current Windows user can connect, and never over the network. The
// protocol is in docs/api.md.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/discover"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

const (
	apiPipe     = `\\.\pipe\syncer`
	apiProtocol = 1
	apiMaxLine  = 1 << 20 // bytes per request
	apiMaxGames = 20000
)

// JSON-RPC error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcFailed         = -32000 // the call was valid but didn't work (message says why)
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ---- what the API reports ----

type apiStatus struct {
	Protocol   int       `json:"protocol"`
	Version    string    `json:"version"`
	Window     bool      `json:"window"`    // served by the Syncer window (false: the --api helper)
	Syncthing  bool      `json:"syncthing"` // Syncthing runs and answers
	Paused     bool      `json:"paused"`    // syncing and automatic backups are paused
	PausedTill time.Time `json:"pausedUntil,omitzero"`
	BackingUp  bool      `json:"backingUp"`
	LastBackup time.Time `json:"lastBackup,omitzero"` // last backup without errors
	Games      int       `json:"games"`
	Conflicts  int       `json:"conflicts"`
}

// apiFolder is one save folder Syncer looks after.
type apiFolder struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Path      string    `json:"path"`
	Sync      bool      `json:"sync"`   // synced with other PCs (false: only backed up)
	Backup    bool      `json:"backup"` // backed up
	State     string    `json:"state"`  // Syncthing's state: idle, scanning, syncing, …; backup-only, off, paused
	NeedBytes int64     `json:"needBytes"`
	Errors    int       `json:"errors"`
	Conflicts int       `json:"conflicts"`
	Exists    bool      `json:"exists"`
	Modified  time.Time `json:"modified,omitzero"`
	BackedUp  time.Time `json:"backedUp,omitzero"`
	NewerOn   string    `json:"newerOn,omitempty"` // another PC backed up a newer save that isn't here yet
	NewerAt   time.Time `json:"newerAt,omitzero"`
}

func folderFromView(v FolderView) apiFolder {
	return apiFolder{ID: v.ID, Label: v.Label, Path: v.Path, Sync: v.Sync, Backup: v.Backup, State: v.State,
		NeedBytes: v.NeedBytes, Errors: v.Errors, Conflicts: v.Conflicts, Exists: v.Exists, Modified: v.Modified,
		BackedUp: v.BackedUp, NewerOn: v.NewerOn, NewerAt: v.NewerAt}
}

type apiSyncResult struct {
	ID        string `json:"id"`
	Done      bool   `json:"done"` // up to date with the other PCs (or nothing to sync)
	State     string `json:"state"`
	NeedBytes int64  `json:"needBytes"`
}

type apiBackupResult struct {
	Started  bool      `json:"started"`
	Finished bool      `json:"finished"`
	OK       bool      `json:"ok"`
	At       time.Time `json:"at,omitzero"`
	Copied   int       `json:"copied"`
	Errors   []string  `json:"errors,omitempty"`
}

// apiGame is a game a launcher knows about, sent with registerGames.
type apiGame struct {
	Title      string `json:"title"`
	Dir        string `json:"dir,omitempty"`
	SteamAppID int    `json:"steamAppId,omitempty"`
	GogID      string `json:"gogId,omitempty"`
}

// apiBackend is what the API asks of Syncer (a fake one in tests).
type apiBackend interface {
	status(ctx context.Context) apiStatus
	folders(ctx context.Context) ([]apiFolder, error)
	syncNow(ctx context.Context, ids []string) []apiSyncResult
	backupNow(ctx context.Context, wait bool) (apiBackupResult, error)
	conflicts(id string) ([]conflict.Conflict, error)
	resolveConflict(id, copyRel string, useCopy bool) error
	open() error
}

// ---- the server ----

type apiServer struct {
	b  apiBackend
	ln net.Listener

	mu       sync.Mutex
	conns    int
	lastUsed time.Time
	subs     map[*apiConn]bool
	games    []apiGame
	gamesAt  string // file registered games are kept in ("" in tests)
	lastSig  string
	stopPoll context.CancelFunc
}

// listenAPI opens the pipe. It fails when another Syncer already serves it.
func listenAPI(name string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	// Protected DACL: full access for this user only (not even administrators).
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

func currentUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

func newAPIServer(b apiBackend, ln net.Listener, gamesFile string) *apiServer {
	s := &apiServer{b: b, ln: ln, subs: map[*apiConn]bool{}, lastUsed: time.Now(), gamesAt: gamesFile}
	if gamesFile != "" {
		if data, err := os.ReadFile(gamesFile); err == nil {
			_ = json.Unmarshal(data, &s.games)
		}
	}
	return s
}

// serve accepts connections until the listener is closed.
func (s *apiServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

// idleFor reports how long nobody has been connected.
func (s *apiServer) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns > 0 {
		return 0
	}
	return time.Since(s.lastUsed)
}

type apiConn struct {
	c  net.Conn
	mu sync.Mutex
}

func (ac *apiConn) send(m rpcMessage) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	_ = ac.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = ac.c.Write(append(b, '\n'))
}

func (s *apiServer) handle(c net.Conn) {
	ac := &apiConn{c: c}
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
		s.mu.Lock()
		s.conns--
		s.lastUsed = time.Now()
		delete(s.subs, ac)
		s.mu.Unlock()
		c.Close()
	}()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), apiMaxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			ac.send(rpcMessage{ID: json.RawMessage("null"), Error: &rpcError{rpcParseError, "not valid JSON"}})
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			ac.send(rpcMessage{ID: idOrNull(req.ID), Error: &rpcError{rpcInvalidRequest, "not a JSON-RPC 2.0 request"}})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, rerr := s.call(ctx, ac, req.Method, req.Params)
			if len(req.ID) == 0 {
				return // a notification: no answer
			}
			if rerr != nil {
				ac.send(rpcMessage{ID: req.ID, Error: rerr})
			} else {
				ac.send(rpcMessage{ID: req.ID, Result: res})
			}
		}()
	}
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func rpcParams[T any](raw json.RawMessage) (T, *rpcError) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, &rpcError{rpcInvalidParams, "bad params: " + err.Error()}
	}
	return v, nil
}

func failed(err error) *rpcError { return &rpcError{rpcFailed, err.Error()} }

// timeoutOf turns a timeoutMs parameter into a duration within limits.
func timeoutOf(ms int, def, most time.Duration) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if ms <= 0 {
		return def
	}
	return min(d, most)
}

func (s *apiServer) call(ctx context.Context, ac *apiConn, method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "status":
		st := s.b.status(ctx)
		st.Protocol, st.Version = apiProtocol, version
		return st, nil

	case "games":
		fs, err := s.b.folders(ctx)
		if err != nil {
			return nil, failed(err)
		}
		if fs == nil {
			fs = []apiFolder{}
		}
		return fs, nil

	case "gameStatus":
		p, perr := rpcParams[apiGame](raw)
		if perr != nil {
			return nil, perr
		}
		if p.Title == "" && p.Dir == "" && p.SteamAppID <= 0 {
			return nil, &rpcError{rpcInvalidParams, "give a title, dir or steamAppId"}
		}
		fs, err := s.b.folders(ctx)
		if err != nil {
			return nil, failed(err)
		}
		mine := s.match(p, fs)
		return map[string]any{"known": len(mine) > 0, "folders": mine}, nil

	case "syncNow":
		p, perr := rpcParams[struct {
			IDs       []string `json:"ids"`
			TimeoutMs int      `json:"timeoutMs"`
		}](raw)
		if perr != nil {
			return nil, perr
		}
		if len(p.IDs) == 0 || len(p.IDs) > 100 {
			return nil, &rpcError{rpcInvalidParams, "give 1 to 100 folder ids"}
		}
		cctx, cancel := context.WithTimeout(ctx, timeoutOf(p.TimeoutMs, 30*time.Second, 2*time.Minute))
		defer cancel()
		return s.b.syncNow(cctx, p.IDs), nil

	case "backupNow":
		p, perr := rpcParams[struct {
			Wait      bool `json:"wait"`
			TimeoutMs int  `json:"timeoutMs"`
		}](raw)
		if perr != nil {
			return nil, perr
		}
		cctx, cancel := context.WithTimeout(ctx, timeoutOf(p.TimeoutMs, 2*time.Minute, 30*time.Minute))
		defer cancel()
		r, err := s.b.backupNow(cctx, p.Wait)
		if err != nil {
			return nil, failed(err)
		}
		return r, nil

	case "conflicts":
		p, perr := rpcParams[struct {
			ID string `json:"id"`
		}](raw)
		if perr != nil {
			return nil, perr
		}
		cs, err := s.b.conflicts(p.ID)
		if err != nil {
			return nil, failed(err)
		}
		if cs == nil {
			cs = []conflict.Conflict{}
		}
		return cs, nil

	case "resolveConflict":
		p, perr := rpcParams[struct {
			ID      string `json:"id"`
			Copy    string `json:"copy"`
			UseCopy bool   `json:"useCopy"`
		}](raw)
		if perr != nil {
			return nil, perr
		}
		if p.ID == "" || p.Copy == "" {
			return nil, &rpcError{rpcInvalidParams, "give the folder id and the conflict copy"}
		}
		if err := s.b.resolveConflict(p.ID, p.Copy, p.UseCopy); err != nil {
			return nil, failed(err)
		}
		return true, nil

	case "open":
		if err := s.b.open(); err != nil {
			return nil, failed(err)
		}
		return true, nil

	case "registerGames":
		p, perr := rpcParams[struct {
			Games []apiGame `json:"games"`
		}](raw)
		if perr != nil {
			return nil, perr
		}
		if len(p.Games) > apiMaxGames {
			return nil, &rpcError{rpcInvalidParams, fmt.Sprintf("at most %d games", apiMaxGames)}
		}
		var keep []apiGame
		for _, g := range p.Games {
			g.Title = strings.TrimSpace(g.Title)
			if g.Title == "" || len(g.Title) > 200 || len(g.Dir) > 1024 || len(g.GogID) > 32 {
				continue
			}
			if g.Dir != "" && !filepath.IsAbs(g.Dir) {
				g.Dir = ""
			}
			keep = append(keep, g)
		}
		s.mu.Lock()
		s.games = keep
		file := s.gamesAt
		s.mu.Unlock()
		if file != "" {
			if err := store.WriteJSON(file, keep); err != nil {
				logx.Printf("api: saving launcher games: %v", err)
			}
		}
		return map[string]int{"games": len(keep)}, nil

	case "subscribe":
		s.subscribe(ac)
		return true, nil
	}
	return nil, &rpcError{rpcMethodNotFound, "unknown method " + method}
}

// match finds the save folders of a game: by its title, the names the game
// database has for its Steam app or install folder, and the title a
// launcher registered for that folder.
func (s *apiServer) match(g apiGame, fs []apiFolder) []apiFolder {
	names := []string{}
	if g.Title != "" {
		names = append(names, g.Title)
	}
	names = append(names, discover.ManifestNames(g.SteamAppID, g.Dir)...)
	if g.Dir != "" {
		s.mu.Lock()
		for _, r := range s.games {
			if r.Dir != "" && strings.EqualFold(filepath.Clean(r.Dir), filepath.Clean(g.Dir)) {
				names = append(names, r.Title)
			}
		}
		s.mu.Unlock()
	}
	out := []apiFolder{}
	for _, f := range fs {
		for _, n := range names {
			if discover.SameGame(f.Label, n) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// subscribe sends the connection a "changed" notification whenever a save
// folder's state changes, until it disconnects. Folders are only checked
// while someone listens.
func (s *apiServer) subscribe(ac *apiConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[ac] = true
	if s.stopPoll != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stopPoll = cancel
	go s.poll(ctx)
}

func (s *apiServer) poll(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		if len(s.subs) == 0 {
			s.stopPoll()
			s.stopPoll = nil
			s.lastSig = ""
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		fs, err := s.b.folders(fctx)
		cancel()
		if err != nil {
			continue
		}
		sig := signature(fs)
		s.mu.Lock()
		changed := s.lastSig != "" && sig != s.lastSig
		s.lastSig = sig
		subs := make([]*apiConn, 0, len(s.subs))
		for c := range s.subs {
			subs = append(subs, c)
		}
		s.mu.Unlock()
		if changed {
			for _, c := range subs {
				c.send(rpcMessage{Method: "changed", Params: map[string]any{}})
			}
		}
	}
}

func signature(fs []apiFolder) string {
	var sb strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&sb, "%s|%s|%d|%d|%d|%d|%s;", f.ID, f.State, f.NeedBytes, f.Conflicts, f.Errors, f.BackedUp.Unix(), f.NewerOn)
	}
	return sb.String()
}

// ---- Syncer as the backend ----

// appBackend serves the API from Syncer itself. window says whether the
// Syncer window runs in this process (then the interface hears about
// changes) or this is the headless --api helper.
type appBackend struct {
	a      *App
	window bool
}

func (b appBackend) status(ctx context.Context) apiStatus {
	s := store.LoadSettings()
	st := store.LoadState()
	out := apiStatus{Window: b.window, Paused: s.Paused(), LastBackup: st.LastSuccess}
	if out.Paused {
		out.PausedTill = s.PausedUntil
	}
	b.a.mu.Lock()
	out.BackingUp = b.a.backingUp
	b.a.mu.Unlock()
	if c, err := b.a.client(); err == nil {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = c.Status(sctx)
		cancel()
		out.Syncthing = err == nil
	}
	if fs, err := b.folders(ctx); err == nil {
		out.Games = len(fs)
		for _, f := range fs {
			out.Conflicts += f.Conflicts
		}
	}
	return out
}

func (b appBackend) folders(context.Context) ([]apiFolder, error) {
	vs, err := b.a.Folders()
	if err != nil {
		return nil, err
	}
	out := make([]apiFolder, 0, len(vs))
	for _, v := range vs {
		out = append(out, folderFromView(v))
	}
	return out, nil
}

// syncNow asks Syncthing to look at each synced folder right away, then
// waits until it has everything the other PCs have.
func (b appBackend) syncNow(ctx context.Context, ids []string) []apiSyncResult {
	out := make([]apiSyncResult, len(ids))
	c, err := b.a.client()
	for i, id := range ids {
		out[i] = apiSyncResult{ID: id, State: "unknown"}
	}
	if err != nil {
		return out
	}
	synced, _ := c.Folders(ctx)
	known := map[string]bool{}
	for _, f := range synced {
		known[f.ID] = !f.Paused
	}
	for i, id := range ids {
		on, ok := known[id]
		switch {
		case !ok:
			out[i].Done, out[i].State = true, "not-synced" // backup-only or unknown: nothing to sync
		case !on:
			out[i].State = "paused"
		default:
			if err := c.Rescan(ctx, id); err != nil {
				logx.Printf("api: rescan %s: %v", id, err)
			}
		}
	}
	// A rescan starts shortly after it's asked for; give it a moment.
	select {
	case <-ctx.Done():
		return out
	case <-time.After(700 * time.Millisecond):
	}
	for {
		pending := false
		for i := range out {
			if out[i].Done || out[i].State == "paused" || out[i].State == "not-synced" {
				continue
			}
			st, err := c.FolderStatus(ctx, out[i].ID)
			if err != nil {
				pending = true
				continue
			}
			out[i].State, out[i].NeedBytes = st.State, st.NeedBytes
			out[i].Done = st.State == "idle" && st.NeedBytes == 0
			pending = pending || !out[i].Done
		}
		if !pending {
			return out
		}
		select {
		case <-ctx.Done():
			return out
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// backupNow starts a backup, like "Back up now". With wait it returns when
// the backup has finished (or the call's time is up).
func (b appBackend) backupNow(ctx context.Context, wait bool) (apiBackupResult, error) {
	var err error
	if b.window {
		err = b.a.BackupNow() // the window shows its progress
	} else {
		err = b.a.backupHeadless()
	}
	if err != nil {
		return apiBackupResult{}, err
	}
	r := apiBackupResult{Started: true}
	if !wait {
		return r, nil
	}
	started := time.Now()
	for {
		b.a.mu.Lock()
		busy := b.a.backingUp
		b.a.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			return r, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
	if last := store.LoadState().LastBackup; last != nil && !last.Finished.Before(started.Add(-time.Second)) {
		r.Finished, r.OK, r.At, r.Copied, r.Errors = true, last.OK, last.Finished, last.Copied, last.Errors
	}
	return r, nil
}

// backupHeadless runs a backup for the --api helper, which has no window
// to report progress to.
func (a *App) backupHeadless() error {
	a.mu.Lock()
	if a.backingUp {
		a.mu.Unlock()
		return errors.New("a backup is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.backingUp, a.cancel = true, cancel
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.backingUp, a.cancel = false, nil
			a.mu.Unlock()
			cancel()
		}()
		if _, err := runBackup(ctx, nil, nil); err != nil {
			logx.Printf("api: backup: %v", err)
		}
	}()
	return nil
}

func (b appBackend) conflicts(id string) ([]conflict.Conflict, error) { return b.a.Conflicts(id) }

func (b appBackend) resolveConflict(id, copyRel string, useCopy bool) error {
	if b.window {
		return b.a.ResolveConflict(id, copyRel, useCopy)
	}
	return b.a.resolveConflict(id, copyRel, useCopy)
}

func (b appBackend) open() error {
	if b.window {
		b.a.showWindow()
		return nil
	}
	// Start the window (or bring it forward when it's already open).
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command(exe).Start()
}

// ---- running it ----

func launcherGamesFile() string { return filepath.Join(paths.AppDir(), "launcher-games.json") }

// serveAPI serves the launcher API for the Syncer window until ctx ends.
// While the --api helper holds the pipe, it tries again every few seconds.
func serveAPI(ctx context.Context, a *App) {
	for {
		ln, err := listenAPI(apiPipe)
		if err == nil {
			s := newAPIServer(appBackend{a: a, window: true}, ln, launcherGamesFile())
			go func() {
				<-ctx.Done()
				ln.Close()
			}()
			logx.Printf("api: serving %s", apiPipe)
			s.serve()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// runAPI is "Syncer.exe --api": the launcher API without a window, for a
// launcher that needs Syncer while its window isn't open. It exits after a
// minute without connections, or right away when Syncer already serves it.
func runAPI() {
	ln, err := listenAPI(apiPipe)
	if err != nil {
		return // the window (or another helper) serves it
	}
	a := NewApp()
	a.ctx = context.Background()
	s := newAPIServer(appBackend{a: a}, ln, launcherGamesFile())
	go s.serve()
	logx.Printf("api: helper serving %s", apiPipe)
	for s.idleFor() < time.Minute {
		time.Sleep(5 * time.Second)
	}
	for {
		a.mu.Lock()
		busy := a.backingUp
		a.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(time.Second)
	}
	ln.Close()
	logx.Printf("api: helper done")
}
