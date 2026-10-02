package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/syncthing"
)

// openExclusive holds a file open the way a running game might.
func openExclusive(p string) (*os.File, error) {
	u, _ := windows.UTF16PtrFromString(p)
	h, err := windows.CreateFile(u, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), p), nil
}

// ---- fakes -----------------------------------------------------------------------

type fakeST struct {
	folders map[string]syncthing.Folder
	failOn  string // "remove", "patch-path", "add": fail that call once
	need    int    // files the shared folder still needs
}

func (f *fakeST) Folders(context.Context) ([]syncthing.Folder, error) {
	var out []syncthing.Folder
	for _, x := range f.folders {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeST) PatchFolder(_ context.Context, id string, patch map[string]any) error {
	x, ok := f.folders[id]
	if !ok {
		return errors.New("no such folder " + id)
	}
	if p, ok := patch["path"].(string); ok {
		if f.failOn == "patch-path" {
			f.failOn = ""
			return errors.New("injected")
		}
		x.Path = p
	}
	if v, ok := patch["paused"].(bool); ok {
		x.Paused = v
	}
	f.folders[id] = x
	return nil
}

func (f *fakeST) FolderStatus(_ context.Context, id string) (syncthing.FolderStatus, error) {
	n := 0
	if x, ok := f.folders[id]; ok {
		_ = filepath.WalkDir(x.Path, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && !strings.Contains(p, ".stfolder") {
				n++
			}
			return nil
		})
	}
	return syncthing.FolderStatus{State: "idle", NeedFiles: f.need, GlobalFiles: n}, nil
}

func (f *fakeST) RemoveFolder(_ context.Context, id string) error {
	if f.failOn == "remove" {
		f.failOn = ""
		return errors.New("injected")
	}
	delete(f.folders, id)
	return nil
}

type world struct {
	t    *testing.T
	root string
	live string
	hist string
	st   *fakeST
	env  Env
	n    int
	held bool // the (simulated) backup lock
}

const game = "stardew"

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, root: root, live: filepath.Join(root, "Saves", "Stardew"), hist: filepath.Join(root, "history"),
		st: &fakeST{folders: map[string]syncthing.Folder{}}}
	oldDir, oldLocal, oldResolve := dir, localRoot, resolveLive
	dir = func() string { return filepath.Join(root, "appdata") }
	localRoot = func() string { return filepath.Join(root, "local") }
	resolveLive = func(r Record) (string, bool) { return filepath.Join(root, filepath.FromSlash(r.Rel)), true }
	t.Cleanup(func() { dir, localRoot, resolveLive = oldDir, oldLocal, oldResolve; crashHook = nil })
	_ = os.MkdirAll(dir(), 0o755)
	w.env = Env{
		ST: w.st,
		Me: "PC1",
		AddFolder: func(_ context.Context, id, label, path string) error {
			if w.st.failOn == "add" {
				w.st.failOn = ""
				return errors.New("injected")
			}
			if err := os.MkdirAll(filepath.Join(path, ".stfolder"), 0o755); err != nil {
				return err
			}
			w.st.folders[id] = syncthing.Folder{ID: id, Label: label, Path: path}
			return nil
		},
		Snapshot: func(_ context.Context, id, _, path string) error {
			w.n++
			return copyTree(path, filepath.Join(w.hist, fmt.Sprintf("snap-%s-%d", id, w.n)), nil)
		},
		Keep: func(id, abs, rel string) error {
			w.n++
			dst := filepath.Join(w.hist, id, fmt.Sprint(w.n), rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.Rename(abs, dst)
		},
		CopyHistory: func(context.Context, string, string) {},
		HoldBackups: func(context.Context) (func(), error) {
			if w.held {
				t.Fatal("backup lock taken twice")
			}
			w.held = true
			return func() { w.held = false }, nil
		},
	}
	snap, add := w.env.Snapshot, w.env.AddFolder
	w.env.Snapshot = func(ctx context.Context, id, label, path string) error {
		if w.held {
			t.Fatal("restore point taken while holding the backup lock (it would wait forever)")
		}
		return snap(ctx, id, label, path)
	}
	w.env.AddFolder = func(ctx context.Context, id, label, path string) error {
		if w.held {
			t.Fatal("folder added while holding the backup lock (its restore point would wait forever)")
		}
		return add(ctx, id, label, path)
	}
	return w
}

func (w *world) write(rel, content string) {
	w.t.Helper()
	p := filepath.Join(w.live, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func readAt(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// contents hashes every file under root (by content only).
func contents(t *testing.T, root string) map[[32]byte]string {
	t.Helper()
	m := map[[32]byte]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			m[sha256.Sum256(b)] = p
		}
		return nil
	})
	return m
}

// setup makes a shared game with two accounts and a conflict: Alice's
// version is the real file, Bob's the conflict copy.
func (w *world) setup() (alice, bob string) {
	w.write(".stfolder/x", "")
	w.write("slot1.sav", "alice slot 1")
	w.write("sub/common.dat", "common")
	w.write("slot1.sync-conflict-20260101-120000-BOBBBBB.sav", "bob slot 1")
	w.st.folders[game] = syncthing.Folder{ID: game, Label: "Stardew Valley", Path: w.live}
	now := time.Now()
	alice, bob = "aaaaaa", "bbbbbb"
	if _, err := Update(func(s *State) error {
		s.Accounts = []Account{{ID: alice, Name: "Alice", Created: now, Updated: now},
			{ID: bob, Name: "Bob", Created: now.Add(time.Second), Updated: now}}
		s.SetActive(alice, now)
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	return alice, bob
}

func (w *world) splitRecord(alice, bob string) Record {
	return Record{Kind: KindSplit, Game: game, Label: "Stardew Valley", Root: "home", Rel: "Saves/Stardew",
		Accounts: []string{alice, bob}, Assign: map[string]string{"slot1.sync-conflict-20260101-120000-BOBBBBB.sav": bob},
		Files: 2, Gen: 1, Created: time.Now(), By: "PC1"}
}

// ---- tests -----------------------------------------------------------------------

func TestParseFolderID(t *testing.T) {
	cases := map[string]bool{"stardew.u-abcdef": true, "stardew": false, "stardew.u-ABCDEF": false,
		".u-abcdef": false, "a.u-b.u-abcdef": false, "stardew.u-abcde1": false}
	for id, want := range cases {
		g, a, ok := ParseFolderID(id)
		if ok != want {
			t.Errorf("ParseFolderID(%q) ok = %v", id, ok)
		}
		if ok && FolderID(g, a) != id {
			t.Errorf("round trip %q", id)
		}
	}
}

func TestCombine(t *testing.T) {
	newWorld(t)
	t0 := time.Now()
	s := State{Shared: Shared{Accounts: []Account{{ID: "aaaaaa", Name: "Alice", Created: t0, Updated: t0}}}}
	peer := Shared{
		Accounts: []Account{
			{ID: "aaaaaa", Name: "Alicia", Created: t0, Updated: t0.Add(time.Minute)}, // newer rename wins
			{ID: "bbbbbb", Name: "Bob", Created: t0, Updated: t0},
			{ID: "BAD!!!", Name: "x", Updated: t0},                     // invalid id
			{ID: "cccccc", Name: strings.Repeat("x", 99), Updated: t0}, // name too long
			{ID: "dddddd", Name: "Dee", Color: "red", Updated: t0},     // bad color
		},
		Records: []Record{
			{Kind: KindSplit, Game: "g", Label: "G", Rel: "G", Accounts: []string{"aaaaaa", "bbbbbb"}, Gen: 2, Created: t0},
			{Kind: KindSplit, Game: "../evil", Rel: "E", Accounts: []string{"aaaaaa"}, Gen: 1, Created: t0},
			{Kind: KindSplit, Game: "h", Rel: "H", Accounts: []string{"aaaaaa"}, Gen: 1, Created: t0,
				Assign: map[string]string{"../x.sync-conflict-20260101-120000-ABCDEFG.sav": "aaaaaa"}},
			{Kind: KindMerge, Game: "m", Rel: "M", Accounts: []string{"aaaaaa"}, Winner: "bbbbbb", Gen: 1, Created: t0},
		},
	}
	if !s.Combine([]Shared{peer}) {
		t.Fatal("no change")
	}
	if n := s.Name("aaaaaa"); n != "Alicia" {
		t.Errorf("rename: %q", n)
	}
	if len(s.Accounts) != 2 || len(s.Records) != 1 || s.Records[0].Game != "g" {
		t.Errorf("accepted invalid data: %+v", s)
	}
	older := Record{Kind: KindMerge, Game: "g", Rel: "G", Accounts: []string{"aaaaaa"}, Winner: "aaaaaa", Gen: 1, Created: t0}
	if s.Combine([]Shared{{Records: []Record{older}}}) {
		t.Error("an older record replaced a newer one")
	}
	del := Account{ID: "bbbbbb", Name: "Bob", Created: t0, Updated: t0.Add(time.Hour), Deleted: true}
	s.Combine([]Shared{{Accounts: []Account{del}}})
	if _, ok := s.Get("bbbbbb"); ok {
		t.Error("delete not applied")
	}
}

func TestActiveDefaultAndLog(t *testing.T) {
	t0 := time.Now()
	s := State{Shared: Shared{Accounts: []Account{
		{ID: "bbbbbb", Name: "B", Created: t0.Add(time.Hour), Updated: t0},
		{ID: "aaaaaa", Name: "A", Created: t0, Updated: t0}}}}
	if s.ActiveID() != "aaaaaa" {
		t.Errorf("default active = %q", s.ActiveID())
	}
	s.SetActive("bbbbbb", t0.Add(2*time.Hour))
	s.SetActive("aaaaaa", t0.Add(4*time.Hour))
	log := s.ValidLog()
	if ActiveAt(log, t0.Add(3*time.Hour)) != "bbbbbb" || ActiveAt(log, t0.Add(5*time.Hour)) != "aaaaaa" || ActiveAt(log, t0) != "" {
		t.Error("ActiveAt")
	}
}

func TestSplitSwitchMerge(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	before := contents(t, w.live)
	ctx := context.Background()

	if err := Start(ctx, w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	vault := VaultDir(w.live, bob, game)
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "alice slot 1" {
		t.Errorf("live slot1 = %q", got)
	}
	if exists(filepath.Join(w.live, "slot1.sync-conflict-20260101-120000-BOBBBBB.sav")) {
		t.Error("conflict copy still in the live folder")
	}
	if got := readAt(t, filepath.Join(vault, "slot1.sav")); got != "bob slot 1" {
		t.Errorf("bob's slot1 = %q", got)
	}
	if got := readAt(t, filepath.Join(vault, "sub", "common.dat")); got != "common" {
		t.Errorf("bob's common = %q", got)
	}
	if _, ok := w.st.folders[game]; ok {
		t.Error("shared folder still there")
	}
	if f := w.st.folders[FolderID(game, alice)]; !samePath(f.Path, w.live) {
		t.Errorf("alice's folder at %q", f.Path)
	}
	if f := w.st.folders[FolderID(game, bob)]; !samePath(f.Path, vault) {
		t.Errorf("bob's folder at %q", f.Path)
	}
	if s := Load(); s.Applied[game] == "" || len(s.Pending()) != 0 {
		t.Errorf("applied = %v", s.Applied)
	}
	if _, ok := PendingOp(); ok {
		t.Error("journal left behind")
	}
	checkNoLoss(t, before, w.root)

	// Switch to Bob and back.
	if err := Switch(ctx, w.env, bob); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "bob slot 1" {
		t.Errorf("after switch live slot1 = %q", got)
	}
	aliceVault := VaultDir(w.live, alice, game)
	if got := readAt(t, filepath.Join(aliceVault, "slot1.sav")); got != "alice slot 1" {
		t.Errorf("alice's vault slot1 = %q", got)
	}
	if f := w.st.folders[FolderID(game, bob)]; !samePath(f.Path, w.live) || f.Paused {
		t.Errorf("bob's folder %+v", f)
	}
	if f := w.st.folders[FolderID(game, alice)]; !samePath(f.Path, aliceVault) || f.Paused {
		t.Errorf("alice's folder %+v", f)
	}
	if Load().ActiveID() != bob {
		t.Error("active not recorded")
	}
	if err := Switch(ctx, w.env, alice); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "alice slot 1" {
		t.Errorf("after switching back live slot1 = %q", got)
	}
	checkNoLoss(t, before, w.root)

	// Merge with Bob's saves.
	m := Record{Kind: KindMerge, Game: game, Label: "Stardew Valley", Rel: "Saves/Stardew", Accounts: []string{alice, bob},
		Winner: bob, Gen: 2, Created: time.Now(), By: "PC1"}
	if err := Start(ctx, w.env, m); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "bob slot 1" {
		t.Errorf("merged slot1 = %q", got)
	}
	if f, ok := w.st.folders[game]; !ok || !samePath(f.Path, w.live) {
		t.Errorf("shared folder not back: %+v", w.st.folders)
	}
	for _, a := range []string{alice, bob} {
		if _, ok := w.st.folders[FolderID(game, a)]; ok {
			t.Errorf("account folder %s still synced", a)
		}
	}
	if exists(aliceVault) {
		t.Error("alice's vault not moved to .trash")
	}
	checkNoLoss(t, before, w.root) // alice's saves: restore point and .trash
}

// checkNoLoss: every file content that existed before is still somewhere.
func checkNoLoss(t *testing.T, before map[[32]byte]string, root string) {
	t.Helper()
	after := contents(t, root)
	for h, p := range before {
		if _, ok := after[h]; !ok {
			t.Errorf("content of %s is gone", p)
		}
	}
}

func TestSplitRefusesOpenFiles(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	f, err := openExclusive(filepath.Join(w.live, "slot1.sav"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := w.st.folders[game]; !ok || w.st.folders[game].Paused {
		t.Error("shared folder changed")
	}
	if len(Load().Records) != 0 {
		t.Error("record shared although nothing happened")
	}
}

func TestSplitRetriesAfterFailure(t *testing.T) {
	for _, fail := range []string{"remove", "add"} {
		t.Run(fail, func(t *testing.T) {
			w := newWorld(t)
			alice, bob := w.setup()
			before := contents(t, w.live)
			w.st.failOn = fail
			if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err == nil {
				t.Fatal("no error")
			}
			if _, ok := PendingOp(); !ok {
				t.Fatal("journal gone")
			}
			if _, err := Apply(context.Background(), w.env); err != nil {
				t.Fatal(err)
			}
			if got := readAt(t, filepath.Join(VaultDir(w.live, bob, game), "slot1.sav")); got != "bob slot 1" {
				t.Errorf("bob's slot1 = %q", got)
			}
			if _, ok := w.st.folders[FolderID(game, alice)]; !ok {
				t.Error("alice's folder missing")
			}
			checkNoLoss(t, before, w.root)
		})
	}
}

// TestCrashAnywhere kills every operation after each journal write in turn
// and checks that the next start finishes it with nothing lost.
func TestCrashAnywhere(t *testing.T) {
	type crash struct{}
	ops := []struct {
		name string
		run  func(w *world, alice, bob string) error
	}{
		{"split", func(w *world, alice, bob string) error {
			return Start(context.Background(), w.env, w.splitRecord(alice, bob))
		}},
		{"switch", func(w *world, alice, bob string) error { return Switch(context.Background(), w.env, bob) }},
		{"merge", func(w *world, alice, bob string) error {
			return Start(context.Background(), w.env, Record{Kind: KindMerge, Game: game, Label: "Stardew Valley",
				Rel: "Saves/Stardew", Accounts: []string{alice, bob}, Winner: bob, Gen: 2, Created: time.Now(), By: "PC1"})
		}},
	}
	for _, op := range ops {
		for at := 1; ; at++ {
			w := newWorld(t)
			alice, bob := w.setup()
			before := contents(t, w.live)
			if op.name != "split" {
				if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
					t.Fatal(err)
				}
			}
			n := 0
			crashHook = func(*Journal) {
				if n++; n == at {
					panic(crash{})
				}
			}
			crashed := func() (c bool) {
				defer func() {
					if r := recover(); r != nil {
						if _, ok := r.(crash); !ok {
							panic(r)
						}
						c = true
					}
				}()
				if err := op.run(w, alice, bob); err != nil {
					t.Fatalf("%s: %v", op.name, err)
				}
				return false
			}()
			crashHook = nil
			w.held = false // the OS drops a dead process's locks
			if !crashed {
				t.Logf("%s: crashed at each of %d journal writes", op.name, at-1)
				break // ran to the end without reaching crash point at
			}
			if _, err := Apply(context.Background(), w.env); err != nil {
				t.Fatalf("%s, crash %d: resume: %v", op.name, at, err)
			}
			if op.name == "switch" {
				if Load().ActiveID() != bob {
					// A switch interrupted before the journal: it can just be run again.
					if err := Switch(context.Background(), w.env, bob); err != nil {
						t.Fatalf("switch, crash %d: again: %v", at, err)
					}
				}
			}
			if _, ok := PendingOp(); ok {
				t.Fatalf("%s, crash %d: journal left", op.name, at)
			}
			checkNoLoss(t, before, w.root)
			want := map[string]string{"split": "alice slot 1", "switch": "bob slot 1", "merge": "bob slot 1"}[op.name]
			if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != want {
				t.Errorf("%s, crash %d: live slot1 = %q, want %q", op.name, at, got, want)
			}
			for id, f := range w.st.folders {
				if f.Paused {
					t.Errorf("%s, crash %d: %s left paused", op.name, at, id)
				}
				if !exists(filepath.Join(f.Path, ".stfolder")) {
					t.Errorf("%s, crash %d: %s at %s has no marker", op.name, at, id, f.Path)
				}
			}
			if at > 50 {
				t.Fatal("never finished")
			}
		}
	}
}

func TestMoveDirCrossCheck(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a")
	_ = os.MkdirAll(filepath.Join(src, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "d", "f"), []byte("x"), 0o644)
	dst := filepath.Join(root, "b", "c")
	if err := moveDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if readAt(t, filepath.Join(dst, "d", "f")) != "x" || exists(src) {
		t.Error("move")
	}
	if err := moveDir(dst, dst); err == nil {
		t.Error("moved onto an existing folder")
	}
}

// Another PC's split waits until this PC has the same saves (or an hour).
func TestSplitFromPeerWaitsForSync(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	r := w.splitRecord(alice, bob)
	r.By = "PC2"
	if _, err := Update(func(s *State) error { s.Put(r); return nil }); err != nil {
		t.Fatal(err)
	}
	w.st.need = 1
	if _, err := Apply(context.Background(), w.env); !errors.Is(err, ErrWaiting) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := w.st.folders[game]; !ok || w.st.folders[game].Paused {
		t.Fatal("shared folder touched while waiting")
	}
	w.st.need = 0
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.st.folders[FolderID(game, alice)]; !ok {
		t.Error("not split once in sync")
	}
}

// A folder paused before (e.g. "Pause syncing") stays paused after a switch.
func TestSwitchKeepsPausedFolders(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{alice, bob} {
		f := w.st.folders[FolderID(game, a)]
		f.Paused = true
		w.st.folders[f.ID] = f
	}
	if err := Switch(context.Background(), w.env, bob); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{alice, bob} {
		if !w.st.folders[FolderID(game, a)].Paused {
			t.Errorf("%s's folder was resumed", a)
		}
	}
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "bob slot 1" {
		t.Errorf("live slot1 = %q", got)
	}
}

// TestRandomOperations runs random sequences of edits, splits, switches,
// merges and crashes, and checks after each that no file content that ever
// existed is gone, that no folder is left paused, and that every folder
// Syncthing knows sits at a folder with its marker.
func TestRandomOperations(t *testing.T) {
	type crash struct{}
	seeds := 25
	if testing.Short() {
		seeds = 5
	}
	for seed := 1; seed <= seeds; seed++ {
		w := newWorld(t)
		alice, bob := w.setup()
		rnd := newRand(uint64(seed))
		seen := contents(t, w.live)
		gen := 1
		for step := 0; step < 30; step++ {
			// Crash at a random journal write about a third of the time.
			if rnd.n(3) == 0 {
				at, n := 1+rnd.n(8), 0
				crashHook = func(*Journal) {
					if n++; n == at {
						panic(crash{})
					}
				}
			}
			op := rnd.n(5)
			func() {
				defer func() {
					if r := recover(); r != nil {
						if _, ok := r.(crash); !ok {
							panic(r)
						}
						w.held = false
					}
				}()
				s := Load()
				_, split := s.Split(game)
				switch op {
				case 0: // someone plays: a save changes wherever the live folder is
					// (The game replacing its own save isn't Syncer losing it.)
					name := fmt.Sprintf("slot%d.sav", rnd.n(3))
					if b, err := os.ReadFile(filepath.Join(w.live, name)); err == nil {
						if other := countContent(t, w.root, sha256.Sum256(b)); other <= 1 {
							delete(seen, sha256.Sum256(b))
						}
					}
					w.write(name, fmt.Sprintf("seed %d step %d", seed, step))
				case 1:
					if !split {
						r := w.splitRecord(alice, bob)
						r.Assign, gen = nil, gen+1
						r.Gen = s.NextGen(game)
						_ = Start(context.Background(), w.env, r)
					}
				case 2, 3:
					if split {
						to := alice
						if rnd.n(2) == 0 {
							to = bob
						}
						_ = Switch(context.Background(), w.env, to)
					}
				case 4:
					if split {
						r, _ := s.Split(game)
						m := r
						m.Kind, m.Winner, m.Assign, m.Gen, m.Created = KindMerge, []string{alice, bob}[rnd.n(2)], nil, r.Gen+1, time.Now()
						_ = Start(context.Background(), w.env, m)
					}
				}
			}()
			crashHook = nil
			for h, p := range contents(t, w.live) {
				seen[h] = p
			}
			// The next start finishes whatever was interrupted.
			if _, err := Apply(context.Background(), w.env); err != nil {
				t.Fatalf("seed %d step %d: resume: %v", seed, step, err)
			}
			checkNoLoss(t, seen, w.root)
			if _, ok := PendingOp(); ok {
				t.Fatalf("seed %d step %d: operation left unfinished", seed, step)
			}
			live := 0
			for id, f := range w.st.folders {
				if f.Paused {
					t.Fatalf("seed %d step %d: %s left paused", seed, step, id)
				}
				if !exists(filepath.Join(f.Path, ".stfolder")) {
					t.Fatalf("seed %d step %d: %s at %s has no marker", seed, step, id, f.Path)
				}
				if samePath(f.Path, w.live) {
					live++
				}
			}
			if live != 1 {
				t.Fatalf("seed %d step %d: %d folders at the save folder: %+v", seed, step, live, w.st.folders)
			}
			if t.Failed() {
				t.FailNow()
			}
		}
	}
}

// newRand is a tiny deterministic generator (math/rand/v2's PCG would do,
// but this keeps the sequence stable across Go versions).
type xorshift struct{ s uint64 }

func newRand(seed uint64) *xorshift { return &xorshift{s: seed*2654435761 + 1} }

func (x *xorshift) n(k int) int {
	x.s ^= x.s << 13
	x.s ^= x.s >> 7
	x.s ^= x.s << 17
	return int(x.s % uint64(k))
}

// countContent counts the files under root with the given content.
func countContent(t *testing.T, root string, h [32]byte) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, err := os.ReadFile(p); err == nil && sha256.Sum256(b) == h {
				n++
			}
		}
		return nil
	})
	return n
}

// Another PC's split: only the account playing here keeps this PC's saves;
// the others' folders start empty and fill from the PC that decided.
func TestSplitFromPeerSeedsOnlyItsOwnAccount(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	w.write("slot2.sav", "played offline here")
	r := w.splitRecord(alice, bob)
	r.By, r.Created = "PC2", time.Now().Add(-2*catchUp)
	if _, err := Update(func(s *State) error { s.Put(r); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	vault := VaultDir(w.live, bob, game)
	if exists(filepath.Join(vault, "slot2.sav")) || exists(filepath.Join(vault, "slot1.sav")) {
		t.Error("this PC's saves were copied into bob's folder")
	}
	if got := readAt(t, filepath.Join(w.live, "slot2.sav")); got != "played offline here" {
		t.Errorf("alice's slot2 = %q", got)
	}
	// Bob's saves haven't arrived: switching to him must wait.
	if err := Switch(context.Background(), w.env, bob); err == nil || !strings.Contains(err.Error(), "haven't reached") {
		t.Errorf("switch before bob's saves arrived: %v", err)
	}
}

// A switch that fails on its second game keeps its journal and finishes
// later; the account playing isn't changed until every game switched.
func TestPartialSwitchResumes(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	// A second split game.
	other := filepath.Join(w.root, "Saves", "Other")
	_ = os.MkdirAll(filepath.Join(other, ".stfolder"), 0o755)
	_ = os.WriteFile(filepath.Join(other, "o.sav"), []byte("alice other"), 0o644)
	w.st.folders["other"] = syncthing.Folder{ID: "other", Label: "Other", Path: other}
	r2 := Record{Kind: KindSplit, Game: "other", Label: "Other", Rel: "Saves/Other", Accounts: []string{alice, bob},
		Files: 1, Gen: 1, Created: time.Now(), By: "PC1"}
	if err := Start(context.Background(), w.env, r2); err != nil {
		t.Fatal(err)
	}
	// Hold a file open in the second game's folder once the first swapped.
	var f *os.File
	crashHook = func(j *Journal) {
		if j.Kind == "switch" && f == nil && len(j.Moves) == 2 && j.Moves[0].Step == swapDone {
			f, _ = openExclusive(filepath.Join(j.Moves[1].Live, map[bool]string{true: "o.sav", false: "slot1.sav"}[j.Moves[1].Game == "other"]))
		}
	}
	err := Switch(context.Background(), w.env, bob)
	crashHook = nil
	if f == nil {
		t.Skip("didn't reach the second game")
	}
	f.Close()
	if err == nil {
		t.Fatal("no error")
	}
	if _, ok := PendingOp(); !ok {
		t.Fatal("journal dropped after one game switched")
	}
	if Load().ActiveID() != bob {
		t.Error("a game holds bob's saves, but bob isn't shown as playing")
	}
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	if Load().ActiveID() != bob {
		t.Error("not switched after resuming")
	}
	if readAt(t, filepath.Join(w.live, "slot1.sav")) != "bob slot 1" || !samePath(w.st.folders[FolderID("other", bob)].Path, other) {
		t.Error("games not switched")
	}
	for id, x := range w.st.folders {
		if x.Paused {
			t.Errorf("%s left paused", id)
		}
	}
}

// A merge that can't move the saves keeps its folders paused and retries.
func TestMergeFailureStaysPaused(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	var f *os.File
	crashHook = func(j *Journal) {
		if j.Kind == KindMerge && f == nil && len(j.Moves) == 1 && j.Moves[0].Step == swapHold {
			f, _ = openExclusive(filepath.Join(w.live, "slot1.sav"))
		}
	}
	m := Record{Kind: KindMerge, Game: game, Label: "Stardew Valley", Rel: "Saves/Stardew", Accounts: []string{alice, bob},
		Winner: bob, Gen: 2, Created: time.Now(), By: "PC1"}
	err := Start(context.Background(), w.env, m)
	crashHook = nil
	if f == nil {
		t.Fatal("didn't reach the swap")
	}
	if err == nil {
		t.Fatal("no error")
	}
	for _, a := range []string{alice, bob} {
		if !w.st.folders[FolderID(game, a)].Paused {
			t.Errorf("%s's folder resumed while the merge is unfinished", a)
		}
	}
	f.Close()
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, filepath.Join(w.live, "slot1.sav")); got != "bob slot 1" {
		t.Errorf("merged slot1 = %q", got)
	}
}

// A peer's merge waits for the winner's saves; if they never come, the
// shared folder starts empty instead of with the wrong person's saves.
func TestMergeFromPeerWithoutWinner(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	delete(w.st.folders, FolderID(game, bob)) // bob's saves never got here
	m := Record{Kind: KindMerge, Game: game, Label: "Stardew Valley", Rel: "Saves/Stardew", Accounts: []string{alice, bob},
		Winner: bob, Gen: 2, Created: time.Now(), By: "PC2"}
	if _, err := Update(func(s *State) error { s.Put(m); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), w.env); !errors.Is(err, ErrWaiting) {
		t.Fatalf("err = %v", err)
	}
	m.Gen, m.Created = 3, time.Now().Add(-2*catchUp)
	if _, err := Update(func(s *State) error { s.Put(m); return nil }); err != nil {
		t.Fatal(err)
	}
	before := contents(t, w.live)
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.live, "slot1.sav")) {
		t.Error("alice's saves became the shared ones")
	}
	if _, ok := w.st.folders[game]; !ok {
		t.Error("shared folder not added")
	}
	checkNoLoss(t, before, w.root)
}

// An account added to a split never beats a merge decided meanwhile.
func TestMergeBeatsConcurrentAddition(t *testing.T) {
	t0 := time.Now()
	split := Record{Kind: KindSplit, Game: "g", Rel: "G", Accounts: []string{"aaaaaa", "bbbbbb"}, Gen: 2, Created: t0, By: "PC1"}
	merge := split
	merge.Kind, merge.Winner, merge.Gen, merge.Created = KindMerge, "bbbbbb", 3, t0.Add(time.Minute)
	s := State{Shared: Shared{Accounts: []Account{{ID: "cccccc", Name: "C", Created: t0, Updated: t0}}, Records: []Record{split}}}
	s.EnsureMember("cccccc", "PC2", t0.Add(2*time.Minute)) // later, same Gen as the merge
	ext, _ := s.Record("g")
	if ext.Gen != 3 || ext.ParentGen != 2 || ext.Seeder() != "PC1" {
		t.Fatalf("extension = %+v", ext)
	}
	if Later(ext, merge) || !Later(merge, ext) {
		t.Error("the addition beat the merge")
	}
	ext.Gen = 9 // however high
	if Later(ext, merge) {
		t.Error("a higher Gen addition beat the merge")
	}
}

// A PC splitting only now, whose player is an account added meanwhile:
// the saves here are that player's, and nobody else's folder gets them.
func TestLateSplitNewAccountKeepsOwnSaves(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	carol := "cccccc"
	now := time.Now()
	if _, err := Update(func(s *State) error {
		s.Accounts = append(s.Accounts, Account{ID: carol, Name: "Carol", Created: now, Updated: now})
		s.SetActive(carol, now)
		r := w.splitRecord(alice, bob)
		r.By, r.Origin, r.Created = "PC2", "PC2", now.Add(-2*catchUp)
		s.Put(r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.write("slot1.sav", "carol offline progress")
	if _, err := Apply(context.Background(), w.env); err != nil {
		t.Fatal(err)
	}
	if f := w.st.folders[FolderID(game, carol)]; !samePath(f.Path, w.live) {
		t.Errorf("carol's folder at %q, want the save folder", f.Path)
	}
	for _, a := range []string{alice, bob} {
		if exists(filepath.Join(VaultDir(w.live, a, game), "slot1.sav")) {
			t.Errorf("carol's saves copied into %s's folder", a)
		}
	}
	if readAt(t, filepath.Join(w.live, "slot1.sav")) != "carol offline progress" {
		t.Error("carol's saves changed")
	}
}

func TestSplitCoversAccountFoldersWithItsRestorePoint(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.setup()
	start := time.Now()
	covered := map[string]time.Time{}
	w.env.Protected = func(id, path string, at time.Time) {
		if _, ok := w.st.folders[id]; ok {
			t.Errorf("%s noted after it was added", id)
		}
		covered[id] = at
	}
	if err := Start(context.Background(), w.env, w.splitRecord(alice, bob)); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{alice, bob} {
		at, ok := covered[FolderID(game, a)]
		if !ok || at.Before(start) || at.After(time.Now()) {
			t.Errorf("%s's folder covered at %v (%v)", a, at, ok)
		}
	}
}
