package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// twoPCs is a folder this PC syncs and backs up, with a backup another PC
// (sharing the same backup folder) may also write to.
type twoPCs struct {
	t           *testing.T
	src, target string
	f           Folder
}

func newTwoPCs(t *testing.T) *twoPCs {
	id := "guard-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { os.Remove(indexPath(id)) })
	src := t.TempDir()
	return &twoPCs{t, src, t.TempDir(), Folder{ID: id, Label: "Game", Path: src}}
}

func (p *twoPCs) run() *runResult {
	p.t.Helper()
	res, err := Run(context.Background(), []Folder{p.f}, Options{Target: p.target})
	if err != nil {
		p.t.Fatal(err)
	}
	if !res.OK {
		p.t.Fatalf("errors: %v", res.Errors)
	}
	return &runResult{held: len(res.Held)}
}

type runResult struct{ held int }

func (p *twoPCs) local(rel string) string  { return filepath.Join(p.src, rel) }
func (p *twoPCs) backup(rel string) string { return filepath.Join(p.target, p.f.ID, rel) }

func setTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// This PC was off while another one played and backed up; at logon it backs
// up before Syncthing brought the new save over.
func TestMirrorKeepsNewerFromOtherPC(t *testing.T) {
	p := newTwoPCs(t)
	old := time.Now().Add(-2 * time.Hour)
	write(t, p.local("slot1.sav"), "old")
	setTime(t, p.local("slot1.sav"), old)
	p.run()

	write(t, p.backup("slot1.sav"), "newer from the other PC")
	setTime(t, p.backup("slot1.sav"), time.Now().Add(-time.Minute))
	if r := p.run(); r.held != 1 {
		t.Errorf("held = %d, want 1", r.held)
	}
	if read(t, p.backup("slot1.sav")) != "newer from the other PC" {
		t.Error("an older save replaced another PC's newer one in the backup")
	}

	// Syncthing brings the new save over: nothing to copy, nothing held.
	write(t, p.local("slot1.sav"), "newer from the other PC")
	setTime(t, p.local("slot1.sav"), time.Now().Add(-time.Minute))
	if r := p.run(); r.held != 0 {
		t.Errorf("held = %d after catching up", r.held)
	}
	if pts := Points(p.target, p.f.ID); len(pts) != 0 {
		t.Errorf("the same save was versioned: %v", pts)
	}
}

func TestMirrorDoesNotRetireOthersFiles(t *testing.T) {
	p := newTwoPCs(t)
	write(t, p.local("slot1.sav"), "a")
	write(t, p.local("slot2.sav"), "b")
	p.run()
	write(t, p.backup("slot3.sav"), "the other PC's new slot")
	if err := os.Remove(p.local("slot2.sav")); err != nil {
		t.Fatal(err)
	}
	if r := p.run(); r.held != 1 {
		t.Errorf("held = %d, want 1", r.held)
	}
	if read(t, p.backup("slot3.sav")) != "the other PC's new slot" {
		t.Error("another PC's file was moved away")
	}
	if _, err := os.Stat(p.backup("slot2.sav")); err == nil {
		t.Error("a file deleted here is still in the backup")
	}
}

// Restoring an older save here: the backup holds what this PC wrote, so the
// older save replaces it (the newer one goes into history).
func TestMirrorOverwritesOwnAfterOlderRestore(t *testing.T) {
	p := newTwoPCs(t)
	write(t, p.local("slot1.sav"), "new")
	p.run()
	write(t, p.local("slot1.sav"), "restored older")
	setTime(t, p.local("slot1.sav"), time.Now().Add(-48*time.Hour))
	if r := p.run(); r.held != 0 {
		t.Errorf("held = %d, want 0", r.held)
	}
	if read(t, p.backup("slot1.sav")) != "restored older" {
		t.Error("this PC's own older save wasn't backed up")
	}
}

func TestKeep(t *testing.T) {
	target := t.TempDir()
	src := filepath.Join(t.TempDir(), "save.dat")
	write(t, src, "x")
	if err := Keep(target, "game", src, "save.dat", false); err != nil {
		t.Fatal(err)
	}
	if read(t, src) != "x" {
		t.Error("a copy left the file")
	}
	pts := Points(target, "game")
	if len(pts) != 1 || !pins(filepath.Join(target, VersionsDir, "game"))[pts[0].Format(stampFmt)] {
		t.Fatalf("want one pinned point, got %v", pts)
	}
	// A second Keep in the same second gets a point of its own.
	if err := Keep(target, "game", src, "save.dat", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("move left the file")
	}
	if n := len(Points(target, "game")); n != 2 {
		t.Errorf("points = %d, want 2", n)
	}

	unlock, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := keepWait
	keepWait = 100 * time.Millisecond
	defer func() { keepWait = old }()
	write(t, src, "y")
	if err := Keep(target, "game", src, "save.dat", true); err != ErrBusy {
		t.Errorf("Keep during a backup = %v, want ErrBusy", err)
	}
}

func TestMergeHistory(t *testing.T) {
	local, target := t.TempDir(), t.TempDir()
	write(t, filepath.Join(local, VersionsDir, "game", "2026-09-01_120000", "save.dat"), "before syncing")
	write(t, filepath.Join(local, VersionsDir, "game", PinnedDir, "2026-09-01_120000"), "")
	write(t, filepath.Join(target, VersionsDir, "game", "2026-09-02_120000", "save.dat"), "later")
	if err := MergeHistory(local, target); err != nil {
		t.Fatal(err)
	}
	if n := len(Points(target, "game")); n != 2 {
		t.Errorf("points = %d, want 2", n)
	}
	if !pins(filepath.Join(target, VersionsDir, "game"))["2026-09-01_120000"] {
		t.Error("pin not moved along")
	}
	if _, err := os.Stat(local); err == nil {
		t.Error("the local folder is still there")
	}
	if err := MergeHistory(local, target); err != nil {
		t.Errorf("nothing left to merge: %v", err)
	}
}

// A backed-up-only game: only this PC writes its backup, so nothing is held.
func TestMirrorSoloHoldsNothing(t *testing.T) {
	p := newTwoPCs(t)
	p.f.Solo = true
	write(t, p.local("slot1.sav"), "mine, older")
	setTime(t, p.local("slot1.sav"), time.Now().Add(-time.Hour))
	write(t, p.backup("slot1.sav"), "seeded, newer")
	write(t, p.backup("seeded-only.sav"), "x")
	if r := p.run(); r.held != 0 {
		t.Errorf("held = %d", r.held)
	}
	if read(t, p.backup("slot1.sav")) != "mine, older" {
		t.Error("this PC's save wasn't backed up")
	}
	if _, err := os.Stat(p.backup("seeded-only.sav")); err == nil {
		t.Error("a file this PC doesn't have stayed in its own backup")
	}
}
