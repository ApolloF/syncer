package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestFileList(t *testing.T) {
	p := newTwoPCs(t)
	write(t, p.local("slot1.sav"), "one")
	write(t, p.local(filepath.Join("Sub", "Slot2.sav")), "two")
	res, err := Run(context.Background(), []Folder{p.f}, Options{Target: p.target, Device: "DEV-A"})
	if err != nil || !res.OK {
		t.Fatalf("run: %v %v", err, res)
	}
	ins := ReadInfos(p.target, p.f.ID)
	if len(ins) != 1 || ins[0].Device != "DEV-A" || ins[0].Files == "" {
		t.Fatalf("info: %+v", ins)
	}
	files, err := ReadFiles(p.target, ins[0])
	if err != nil || len(files) != 2 || files[0].Path != "Sub/Slot2.sav" || files[1].Path != "slot1.sav" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if !MirrorMatches(p.target, p.f.ID, files) {
		t.Error("the backup doesn't match its own list")
	}
	if !LocalMatchesIndex(p.f) {
		t.Error("nothing changed here since the backup")
	}

	// Drive hasn't brought one file yet.
	write(t, p.backup("slot1.sav"), "on its way")
	if MirrorMatches(p.target, p.f.ID, files) {
		t.Error("a half-arrived backup matches")
	}
	// The list itself is newer than the info file.
	lp := filesPath(p.target, p.f.ID, ins[0].key)
	write(t, lp, `{"id":"`+p.f.ID+`","files":[]}`)
	if _, err := ReadFiles(p.target, ins[0]); err == nil {
		t.Error("a list with another hash was read")
	}

	write(t, p.local("slot1.sav"), "changed here")
	if LocalMatchesIndex(p.f) {
		t.Error("a changed save matches the index")
	}
}

func TestSafeRel(t *testing.T) {
	for rel, ok := range map[string]bool{"a.sav": true, "Sub/b.sav": true, "../x": false, "a/../../x": false,
		`C:\Windows\x`: false, "/abs": false, "": false, "a:b": false, "./a": false} {
		if safeRel(rel) != ok {
			t.Errorf("safeRel(%q) = %v", rel, !ok)
		}
	}
}

func TestTakeFiles(t *testing.T) {
	target := t.TempDir()
	local := t.TempDir()
	f := Folder{ID: "game", Label: "Game", Path: local}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	var files []FileEntry
	for _, n := range []string{"slot1.sav", "Sub/slot2.sav", "same.sav"} {
		src := filepath.Join(target, "game", filepath.FromSlash(n))
		write(t, src, "newer "+n)
		setTime(t, src, at)
		files = append(files, FileEntry{Path: n, Size: int64(len("newer " + n)), MTime: at.UnixMilli()})
	}
	write(t, filepath.Join(local, "slot1.sav"), "older")
	write(t, filepath.Join(local, "same.sav"), "newer same.sav")
	write(t, filepath.Join(local, "only-here.sav"), "stays")

	// The game holds slot1 open: nothing changes.
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(filepath.Join(local, "slot1.sav")), windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TakeFiles(context.Background(), target, f, files); err == nil {
		t.Error("took files while one was open")
	}
	windows.CloseHandle(h)
	if read(t, filepath.Join(local, "slot1.sav")) != "older" {
		t.Error("slot1 changed although taking failed")
	}
	if _, err := os.Stat(filepath.Join(local, "Sub", "slot2.sav")); err == nil {
		t.Error("slot2 left behind by a failed take")
	}

	n, err := TakeFiles(context.Background(), target, f, files)
	if err != nil || n != 2 {
		t.Fatalf("take = %d, %v", n, err)
	}
	if read(t, filepath.Join(local, "slot1.sav")) != "newer slot1.sav" || read(t, filepath.Join(local, "Sub", "slot2.sav")) != "newer Sub/slot2.sav" ||
		read(t, filepath.Join(local, "only-here.sav")) != "stays" {
		t.Error("wrong files after taking")
	}
	if fi, _ := os.Stat(filepath.Join(local, "slot1.sav")); !sameTime(fi.ModTime(), at) {
		t.Error("modification time not kept")
	}
	if es, _ := os.ReadDir(filepath.Join(local, ".stversions")); len(es) != 0 {
		t.Errorf("staging left behind: %v", es)
	}

	// The backup changed after the list was read.
	write(t, filepath.Join(target, "game", "slot1.sav"), "newest")
	if _, err := TakeFiles(context.Background(), target, f, files); err == nil {
		t.Error("took a file that no longer matches the list")
	}
}
