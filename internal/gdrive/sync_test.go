package gdrive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDrive is Google Drive in memory.
type fakeDrive struct {
	files   map[string]File
	data    map[string][]byte
	next    int
	uploads int
	moves   int
	// failDelete makes Delete fail for the files it reports (a rate limit).
	failDelete func(id string) bool
}

func newFake() *fakeDrive { return &fakeDrive{files: map[string]File{}, data: map[string][]byte{}} }

func (d *fakeDrive) id() string { d.next++; return fmt.Sprintf("id%d", d.next) }

func (d *fakeDrive) List(context.Context) ([]File, error) {
	var out []File
	for _, f := range d.files {
		out = append(out, f)
	}
	return out, nil
}

func (d *fakeDrive) FindTop(_ context.Context, name string) ([]File, error) {
	var out []File
	for _, f := range d.files {
		if f.Name == name && f.Folder() && firstParent(f) == "root" {
			out = append(out, f)
		}
	}
	return out, nil
}

func (d *fakeDrive) Mkdir(_ context.Context, parent, name string) (File, error) {
	f := File{ID: d.id(), Name: name, MimeType: folderType, Parents: []string{parent}, Created: time.Now()}
	d.files[f.ID] = f
	return f, nil
}

func (d *fakeDrive) put(id, parent, name string, b []byte, mtime time.Time) File {
	sum := md5.Sum(b)
	f := File{ID: id, Name: name, Parents: []string{parent}, Size: int64(len(b)), MD5: hex.EncodeToString(sum[:]), Modified: mtime.UTC().Truncate(time.Millisecond)}
	d.files[id], d.data[id] = f, b
	return f
}

func (d *fakeDrive) Upload(_ context.Context, parent, name, path string, mtime time.Time) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	d.uploads++
	return d.put(d.id(), parent, name, b, mtime), nil
}

func (d *fakeDrive) Update(_ context.Context, id, path string, mtime time.Time) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	d.uploads++
	f := d.files[id]
	return d.put(id, firstParent(f), f.Name, b, mtime), nil
}

func (d *fakeDrive) Download(_ context.Context, id string, w io.Writer) error {
	_, err := io.Copy(w, bytes.NewReader(d.data[id]))
	return err
}

func (d *fakeDrive) Move(_ context.Context, id, _, to, name string) (File, error) {
	f := d.files[id]
	f.Parents, f.Name = []string{to}, name
	d.files[id] = f
	d.moves++
	return f, nil
}

func (d *fakeDrive) Delete(_ context.Context, id string) error {
	if d.failDelete != nil && d.failDelete(id) {
		return errors.New("rate limit exceeded")
	}
	delete(d.files, id)
	delete(d.data, id)
	return nil
}

// path returns the id of the file at rel below the root folder, "" if none.
func (d *fakeDrive) path(rel string) string {
	var root string
	for id, f := range d.files {
		if f.Name == RootName && firstParent(f) == "root" {
			root = id
		}
	}
	parent := root
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		found := ""
		for id, f := range d.files {
			if f.Name == p && firstParent(f) == parent && (i == len(parts)-1) != f.Folder() {
				found = id
			}
		}
		if found == "" {
			return ""
		}
		parent = found
	}
	return parent
}

type pc struct {
	t     *testing.T
	local string
	state string
}

func newPC(t *testing.T) *pc {
	return &pc{t: t, local: filepath.Join(t.TempDir(), RootName), state: filepath.Join(t.TempDir(), "state.json")}
}

func (p *pc) sync(d drive) Report {
	p.t.Helper()
	old := statePath
	statePath = func() string { return p.state }
	defer func() { statePath = old }()
	rep, err := Sync(context.Background(), d, p.local)
	if err != nil {
		p.t.Fatal(err)
	}
	if len(rep.Errors) > 0 {
		p.t.Logf("errors: %v", rep.Errors)
	}
	return rep
}

// withState runs f with p's sync state as the current one.
func (p *pc) withState(f func()) {
	old := statePath
	statePath = func() string { return p.state }
	defer func() { statePath = old }()
	f()
}

func (p *pc) forget(id string) { p.withState(func() { ForgetFolder(id) }) }

func (p *pc) saved() (st State) {
	p.withState(func() { st = LoadState() })
	return st
}

func (p *pc) write(rel, s string, at time.Time) {
	p.t.Helper()
	f := filepath.Join(p.local, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Chtimes(f, at, at); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pc) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(p.local, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSyncTwoPCs(t *testing.T) {
	d := newFake()
	a, b := newPC(t), newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)

	a.write("game/slot1.sav", "a1", t0)
	a.write(".syncer/game/pc-a.json", "{}", t0)
	if r := a.sync(d); r.Up != 2 {
		t.Fatalf("first sync: %+v", r)
	}
	if r := a.sync(d); r.Up+r.Down+r.Moved+r.DeletedHere+r.DeletedThere != 0 {
		t.Errorf("nothing changed, yet: %+v", r)
	}

	// The other PC gets it, with its time.
	if r := b.sync(d); r.Down != 2 || b.read("game/slot1.sav") != "a1" {
		t.Fatalf("second PC: %+v, %q", r, b.read("game/slot1.sav"))
	}
	if fi, _ := os.Stat(filepath.Join(b.local, "game", "slot1.sav")); !fi.ModTime().Equal(t0) {
		t.Errorf("time not kept: %v", fi.ModTime())
	}

	// B changes the save; A gets it.
	b.write("game/slot1.sav", "b2", t0.Add(time.Minute))
	b.sync(d)
	if a.sync(d); a.read("game/slot1.sav") != "b2" {
		t.Errorf("change not brought over: %q", a.read("game/slot1.sav"))
	}

	// A's backup moves the old save into history: moved in Drive, not sent again.
	uploads := d.uploads
	if err := os.MkdirAll(filepath.Join(a.local, ".versions", "game", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(a.local, "game", "slot1.sav"), filepath.Join(a.local, ".versions", "game", "s1", "slot1.sav")); err != nil {
		t.Fatal(err)
	}
	if r := a.sync(d); r.Moved != 1 || d.uploads != uploads || d.path(".versions/game/s1/slot1.sav") == "" || d.path("game/slot1.sav") != "" {
		t.Errorf("move: %+v, uploads %d->%d", r, uploads, d.uploads)
	}
	// B follows: the old place is deleted there, the new one arrives.
	if b.sync(d); b.read("game/slot1.sav") != "<missing>" || b.read(".versions/game/s1/slot1.sav") != "b2" {
		t.Errorf("B after the move: %q / %q", b.read("game/slot1.sav"), b.read(".versions/game/s1/slot1.sav"))
	}
}

func TestSyncBothChangedNewerWins(t *testing.T) {
	d := newFake()
	a, b := newPC(t), newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a.write("x.json", "old", t0)
	a.sync(d)
	b.sync(d)
	a.write("x.json", "from a", t0.Add(time.Minute))
	b.write("x.json", "from b, later", t0.Add(2*time.Minute))
	a.sync(d)
	b.sync(d)
	a.sync(d)
	if a.read("x.json") != "from b, later" || b.read("x.json") != "from b, later" {
		t.Errorf("a=%q b=%q", a.read("x.json"), b.read("x.json"))
	}
}

// A lost local folder (or an emptied Drive folder) must not delete the other side.
func TestSyncNeverWipesOnEmptySide(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		a.write(fmt.Sprintf("g/%d.sav", i), "x", t0)
	}
	a.sync(d)
	if err := os.RemoveAll(a.local); err != nil {
		t.Fatal(err)
	}
	if r := a.sync(d); r.DeletedThere != 0 || r.Down != 5 {
		t.Errorf("local folder lost: %+v", r)
	}
	for id, f := range d.files {
		if !f.Folder() {
			delete(d.files, id)
		}
	}
	if r := a.sync(d); r.DeletedHere != 0 || r.Up != 5 {
		t.Errorf("Drive folder emptied: %+v", r)
	}
}

func TestSyncMassDeleteInDrive(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < 30; i++ {
		a.write(fmt.Sprintf("g/%d.sav", i), "x", t0)
		a.write(fmt.Sprintf(".versions/g/s/%d.sav", i), "old", t0)
	}
	a.write("keep.txt", "k", t0)
	a.sync(d)

	// Thinning history (another PC's prune) goes through.
	for i := 0; i < 30; i++ {
		_ = d.Delete(context.Background(), d.path(fmt.Sprintf(".versions/g/s/%d.sav", i)))
	}
	if r := a.sync(d); r.DeletedHere != 30 {
		t.Errorf("history thinned in Drive: %+v", r)
	}
	// Most of the backups gone from Drive: they go back up.
	for i := 0; i < 30; i++ {
		_ = d.Delete(context.Background(), d.path(fmt.Sprintf("g/%d.sav", i)))
	}
	if r := a.sync(d); r.DeletedHere != 0 || r.Up != 30 || len(r.Notes) == 0 {
		t.Errorf("backups deleted in Drive: %+v", r)
	}
	// Most of the backups gone from this PC: they come back from Drive.
	remove := func() {
		for i := 0; i < 30; i++ {
			_ = os.Remove(filepath.Join(a.local, "g", fmt.Sprintf("%d.sav", i)))
		}
	}
	remove()
	if r := a.sync(d); r.DeletedThere != 0 || r.Down != 30 || len(r.Notes) == 0 {
		t.Errorf("backups deleted here: %+v", r)
	}
	// This PC deleting a game's backup on purpose goes through.
	remove()
	a.forget("g")
	if r := a.sync(d); r.DeletedThere != 30 {
		t.Errorf("backup removed here: %+v", r)
	}
	if st := a.saved(); len(st.Forgotten) != 0 {
		t.Errorf("still forgotten after the sync: %v", st.Forgotten)
	}
}

// Some of a forgotten folder's files fail to go in Drive: the folder stays
// forgotten, so the rest go next time instead of coming back down.
func TestSyncForgottenFolderPartlyDeleted(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < 30; i++ {
		a.write(fmt.Sprintf("g/%d.sav", i), "x", t0)
	}
	a.write("other/1.sav", "o", t0)
	a.sync(d)

	if err := os.RemoveAll(filepath.Join(a.local, "g")); err != nil {
		t.Fatal(err)
	}
	a.forget("g")
	failing := map[string]bool{}
	for i := 0; i < 10; i++ {
		failing[d.path(fmt.Sprintf("g/%d.sav", i))] = true
	}
	d.failDelete = func(id string) bool { return failing[id] }
	if r := a.sync(d); r.DeletedThere != 20 || len(r.Errors) != 10 {
		t.Fatalf("first sync: %+v", r)
	}
	if st := a.saved(); len(st.Forgotten) != 1 || st.Forgotten[0] != "g" {
		t.Fatalf("forgotten after a partial delete: %v", st.Forgotten)
	}

	d.failDelete = nil
	if r := a.sync(d); r.DeletedThere != 10 || r.Down != 0 {
		t.Errorf("second sync: %+v", r)
	}
	if got := a.read("g/0.sav"); got != "<missing>" {
		t.Errorf("forgotten backup came back: %q", got)
	}
	if st := a.saved(); len(st.Forgotten) != 0 {
		t.Errorf("still forgotten after all files went: %v", st.Forgotten)
	}
}

// Two PCs made the same folder at once: files in either copy count.
func TestSyncDuplicateFolders(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a.write("g/a.sav", "a", t0)
	a.sync(d) // makes GameSaveBackup and g
	// B made its own "g" before it saw A's.
	var g File
	for _, f := range d.files {
		if f.Name == "g" && f.Folder() {
			g = f
		}
	}
	dup, _ := d.Mkdir(context.Background(), firstParent(g), "g")
	d.put(d.id(), dup.ID, "b.sav", []byte("b"), t0)
	if r := a.sync(d); r.Down != 1 || a.read("g/b.sav") != "b" {
		t.Errorf("file in the duplicate folder: %+v", r)
	}
	if r := a.sync(d); r.DeletedHere+r.DeletedThere != 0 {
		t.Errorf("deleted something: %+v", r)
	}
	// A second GameSaveBackup at the top counts as well.
	top, _ := d.Mkdir(context.Background(), "root", RootName)
	d.put(d.id(), top.ID, "top.json", []byte("{}"), t0)
	if a.sync(d); a.read("top.json") != "{}" {
		t.Error("file in the second GameSaveBackup not brought over")
	}
}

// Both PCs changed a backed-up save: the newer wins, the other goes into
// the game's history.
func TestSyncKeepsLosingVersion(t *testing.T) {
	d := newFake()
	a, b := newPC(t), newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a.write("game/slot.sav", "old", t0)
	a.sync(d)
	b.sync(d)
	a.write("game/slot.sav", "a, older", t0.Add(time.Minute))
	b.write("game/slot.sav", "b, newer", t0.Add(2*time.Minute))
	b.sync(d)
	if r := a.sync(d); r.Kept != 1 || a.read("game/slot.sav") != "b, newer" {
		t.Fatalf("a: %+v %q", r, a.read("game/slot.sav"))
	}
	es, _ := filepath.Glob(filepath.Join(a.local, ".versions", "game", "*", "slot.sav"))
	if len(es) != 1 {
		t.Fatalf("losing version not in history: %v", es)
	}
	if b, _ := os.ReadFile(es[0]); string(b) != "a, older" {
		t.Errorf("history holds %q", b)
	}
	if pins, _ := filepath.Glob(filepath.Join(a.local, ".versions", "game", ".pinned", "*")); len(pins) != 1 {
		t.Errorf("not pinned: %v", pins)
	}
}

func TestTreeSkipsUnsafeNames(t *testing.T) {
	byID := map[string]File{
		"r": {ID: "r", Name: RootName, MimeType: folderType, Parents: []string{"root"}},
		"a": {ID: "a", Name: "ok.sav", Parents: []string{"r"}},
		"b": {ID: "b", Name: "..", Parents: []string{"r"}},
		"c": {ID: "c", Name: `x\y`, Parents: []string{"r"}},
		"d": {ID: "d", Name: "elsewhere", Parents: []string{"other"}},
	}
	_, files := tree(byID, map[string]bool{"r": true}, "r")
	if len(files) != 1 || files["ok.sav"].ID != "a" {
		t.Errorf("files: %+v", files)
	}
}

// A game's backup folder gone from this PC (most of the backed-up saves)
// is brought back from Drive, not deleted there.
func TestSyncLocalSubtreeLossKeepsDrive(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 25; i++ {
		a.write(fmt.Sprintf("big/%d.sav", i), "x", t0)
	}
	a.write("small/a.sav", "a", t0)
	a.write("small/b.sav", "b", t0)
	a.sync(d)
	if err := os.RemoveAll(filepath.Join(a.local, "big")); err != nil {
		t.Fatal(err)
	}
	if r := a.sync(d); r.DeletedThere != 0 || r.Down != 25 || len(r.Notes) == 0 {
		t.Errorf("subtree lost here: %+v", r)
	}
	if a.read("big/7.sav") != "x" || d.path("big/7.sav") == "" {
		t.Error("big/7.sav not back on both sides")
	}
	// A few saves deleted here still go.
	_ = os.Remove(filepath.Join(a.local, "small", "a.sav"))
	if r := a.sync(d); r.DeletedThere != 1 {
		t.Errorf("one save deleted here: %+v", r)
	}
}

// One game's backup lost here is held back, however many other games
// there are; a few of its saves deleted, or a small game's, still go.
func TestSyncOneGameLossKeepsDrive(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	for g := 0; g < 10; g++ {
		for i := 0; i < 8; i++ {
			a.write(fmt.Sprintf("g%d/%d.sav", g, i), "x", t0)
		}
	}
	a.write("small/a.sav", "a", t0)
	a.sync(d)
	if err := os.RemoveAll(filepath.Join(a.local, "g3")); err != nil {
		t.Fatal(err)
	}
	if r := a.sync(d); r.DeletedThere != 0 || r.Down != 8 || len(r.Notes) == 0 {
		t.Errorf("one game lost here: %+v", r)
	}
	if a.read("g3/5.sav") != "x" || d.path("g3/5.sav") == "" {
		t.Error("g3/5.sav not back on both sides")
	}
	for i := 0; i < 3; i++ {
		_ = os.Remove(filepath.Join(a.local, "g4", fmt.Sprintf("%d.sav", i)))
	}
	_ = os.Remove(filepath.Join(a.local, "small", "a.sav"))
	if r := a.sync(d); r.DeletedThere != 4 || r.Down != 0 {
		t.Errorf("a few saves deleted here: %+v", r)
	}
}

// A folder that can't be read fails the sync instead of looking emptied.
func TestSyncFailsOnUnreadableFolder(t *testing.T) {
	d := newFake()
	a := newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a.write("g/a.sav", "a", t0)
	a.write("h/b.sav", "b", t0)
	a.sync(d)
	dir := filepath.Join(a.local, "g")
	if out, err := exec.Command("icacls", dir, "/deny", "*S-1-1-0:(RD)").CombinedOutput(); err != nil {
		t.Skipf("can't deny access: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("icacls", dir, "/remove:d", "*S-1-1-0").Run() })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("folder still readable")
	}
	old := statePath
	statePath = func() string { return a.state }
	defer func() { statePath = old }()
	if r, err := Sync(context.Background(), d, a.local); err == nil {
		t.Errorf("sync went ahead: %+v", r)
	}
	if d.path("g/a.sav") == "" {
		t.Error("g/a.sav deleted in Drive")
	}
}

// Each losing version gets a restore point of its own, marked as saved
// outside a backup run, never one that already holds a file.
func TestSyncLosersGetPointsOfTheirOwn(t *testing.T) {
	d := newFake()
	a, b := newPC(t), newPC(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	a.write("game/slot.sav", "old", t0)
	a.write("game/other.sav", "old", t0)
	a.sync(d)
	b.sync(d)
	// Points a backup run made in the next few seconds.
	now := time.Now()
	var taken []string
	for i := range 4 {
		stamp := now.Add(time.Duration(i) * time.Second).Format("2006-01-02_150405")
		a.write(".versions/game/"+stamp+"/slot.sav", "run's", t0)
		taken = append(taken, stamp)
	}
	a.write("game/slot.sav", "a, older", t0.Add(time.Minute))    // loses: kept locally
	a.write("game/other.sav", "a, newer", t0.Add(3*time.Minute)) // wins: Drive's kept
	b.write("game/slot.sav", "b, newer", t0.Add(2*time.Minute))
	b.write("game/other.sav", "b, older", t0.Add(time.Minute))
	b.sync(d)
	if r := a.sync(d); r.Kept != 2 {
		t.Fatalf("a: %+v", r)
	}
	for _, stamp := range taken {
		if got := a.read(".versions/game/" + stamp + "/slot.sav"); got != "run's" {
			t.Errorf("point %s: slot.sav = %q", stamp, got)
		}
		if got := a.read(".versions/game/" + stamp + "/other.sav"); got != "<missing>" {
			t.Errorf("loser put into point %s", stamp)
		}
	}
	aside, _ := filepath.Glob(filepath.Join(a.local, ".versions", "game", ".aside", "*"))
	if len(aside) != 2 {
		t.Fatalf("aside marks: %v", aside)
	}
	got := map[string]bool{}
	for _, m := range aside {
		for _, f := range []string{"slot.sav", "other.sav"} {
			if s := a.read(".versions/game/" + filepath.Base(m) + "/" + f); s != "<missing>" {
				got[f+"="+s] = true
			}
		}
	}
	if !got["slot.sav=a, older"] || !got["other.sav=b, older"] || len(got) != 2 {
		t.Errorf("aside points hold %v", got)
	}
}
