package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"

	"github.com/ApolloF/syncer/internal/conflict"
)

type fakeBackend struct {
	mu     sync.Mutex
	fs     []apiFolder
	synced []string
	opened int
}

func (f *fakeBackend) status(context.Context) apiStatus {
	return apiStatus{Syncthing: true, Games: len(f.fs)}
}
func (f *fakeBackend) folders(context.Context) ([]apiFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiFolder(nil), f.fs...), nil
}
func (f *fakeBackend) syncNow(_ context.Context, ids []string) []apiSyncResult {
	f.synced = append(f.synced, ids...)
	var out []apiSyncResult
	for _, id := range ids {
		out = append(out, apiSyncResult{ID: id, Done: true, State: "idle"})
	}
	return out
}
func (f *fakeBackend) backupNow(context.Context, bool) (apiBackupResult, error) {
	return apiBackupResult{Started: true, Finished: true, OK: true}, nil
}
func (f *fakeBackend) conflicts(id string) ([]conflict.Conflict, error) {
	if id != "a" {
		return nil, fmt.Errorf("unknown folder")
	}
	return []conflict.Conflict{{Rel: "save.dat", Copy: "save.sync-conflict-1.dat"}}, nil
}
func (f *fakeBackend) resolveConflict(string, string, bool) error { return nil }
func (f *fakeBackend) open() error                                { f.opened++; return nil }

type client struct {
	t  *testing.T
	c  net.Conn
	sc *bufio.Scanner
	id int
}

func dial(t *testing.T, name string) *client {
	t.Helper()
	d := 2 * time.Second
	c, err := winio.DialPipe(name, &d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	return &client{t: t, c: c, sc: sc}
}

func (c *client) raw(line string) map[string]any {
	c.t.Helper()
	_ = c.c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.c.Write([]byte(line + "\n")); err != nil {
		c.t.Fatal(err)
	}
	return c.read()
}

func (c *client) read() map[string]any {
	c.t.Helper()
	if !c.sc.Scan() {
		c.t.Fatalf("no answer: %v", c.sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(c.sc.Bytes(), &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *client) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	return c.raw(string(b))
}

func startTest(t *testing.T, b apiBackend) (string, *apiServer) {
	t.Helper()
	name := fmt.Sprintf(`\\.\pipe\syncer-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	ln, err := listenAPI(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := newAPIServer(b, ln, "")
	go s.serve()
	return name, s
}

func TestAPIBasics(t *testing.T) {
	name, _ := startTest(t, &fakeBackend{})
	if _, err := listenAPI(name); err == nil {
		t.Fatal("a second server on the same pipe must fail")
	}
	c := dial(t, name)
	st := c.call("status", nil)["result"].(map[string]any)
	if st["protocol"].(float64) != apiProtocol || st["syncthing"] != true {
		t.Errorf("status = %v", st)
	}
	if e := c.call("nope", nil)["error"].(map[string]any); e["code"].(float64) != rpcMethodNotFound {
		t.Errorf("unknown method = %v", e)
	}
	if e := c.raw("{not json")["error"].(map[string]any); e["code"].(float64) != rpcParseError {
		t.Errorf("bad json = %v", e)
	}
	if e := c.raw(`{"jsonrpc":"1.0","id":1,"method":"status"}`)["error"].(map[string]any); e["code"].(float64) != rpcInvalidRequest {
		t.Errorf("wrong version = %v", e)
	}
	if e := c.call("syncNow", map[string]any{"ids": []string{}})["error"].(map[string]any); e["code"].(float64) != rpcInvalidParams {
		t.Errorf("syncNow without ids = %v", e)
	}
	if e := c.call("conflicts", map[string]any{"id": "zzz"})["error"].(map[string]any); !strings.Contains(e["message"].(string), "unknown") {
		t.Errorf("conflicts error = %v", e)
	}
	// A notification (no id) gets no answer; the next call's answer comes next.
	_, _ = c.c.Write([]byte(`{"jsonrpc":"2.0","method":"open"}` + "\n"))
	if r := c.call("conflicts", map[string]any{"id": "a"}); r["result"] == nil {
		t.Errorf("conflicts = %v", r)
	}
}

func TestAPIGameStatus(t *testing.T) {
	b := &fakeBackend{fs: []apiFolder{
		{ID: "a", Label: "Elden Ring", State: "idle"},
		{ID: "b", Label: "Hollow Knight (RUNE saves)", State: "backup-only"},
		{ID: "c", Label: "Some Other Game"},
	}}
	name, _ := startTest(t, b)
	c := dial(t, name)
	ids := func(r map[string]any) []string {
		res := r["result"].(map[string]any)
		var out []string
		for _, f := range res["folders"].([]any) {
			out = append(out, f.(map[string]any)["id"].(string))
		}
		return out
	}
	if got := ids(c.call("gameStatus", map[string]any{"title": "ELDEN RING"})); len(got) != 1 || got[0] != "a" {
		t.Errorf("by title = %v", got)
	}
	if got := ids(c.call("gameStatus", map[string]any{"title": "Hollow Knight"})); len(got) != 1 || got[0] != "b" {
		t.Errorf("emulator saves = %v", got)
	}
	if got := ids(c.call("gameStatus", map[string]any{"title": "Unknown"})); len(got) != 0 {
		t.Errorf("unknown = %v", got)
	}
	// A launcher registers its folder names; the folder then finds the game.
	c.call("registerGames", map[string]any{"games": []map[string]any{
		{"title": "Some Other Game", "dir": `D:\Games\SOG-Repack`},
		{"title": "", "dir": `D:\x`},
		{"title": "Relative", "dir": `games\x`},
	}})
	if got := ids(c.call("gameStatus", map[string]any{"dir": `d:\games\sog-repack\`})); len(got) != 1 || got[0] != "c" {
		t.Errorf("by registered dir = %v", got)
	}
	if e := c.call("gameStatus", map[string]any{})["error"]; e == nil {
		t.Error("gameStatus without anything must fail")
	}
}

func TestAPISubscribe(t *testing.T) {
	b := &fakeBackend{fs: []apiFolder{{ID: "a", Label: "Game", State: "idle"}}}
	name, _ := startTest(t, b)
	c := dial(t, name)
	if r := c.call("subscribe", nil); r["result"] != true {
		t.Fatalf("subscribe = %v", r)
	}
	time.Sleep(3500 * time.Millisecond) // first poll records the state
	b.mu.Lock()
	b.fs[0].State = "syncing"
	b.mu.Unlock()
	_ = c.c.SetDeadline(time.Now().Add(8 * time.Second))
	if m := c.read(); m["method"] != "changed" {
		t.Errorf("notification = %v", m)
	}
}

func TestTimeoutOf(t *testing.T) {
	if timeoutOf(0, time.Second, time.Minute) != time.Second || timeoutOf(999999999, time.Second, time.Minute) != time.Minute {
		t.Error("timeoutOf limits")
	}
}
