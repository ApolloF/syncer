package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

func TestInfoRoundTripAndForget(t *testing.T) {
	target := t.TempDir()
	docs := paths.Root(paths.Documents)
	f := Folder{ID: "hollow-knight", Label: "Hollow Knight", Path: filepath.Join(docs, "Hollow Knight")}
	newest := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	writeInfo(target, f, newest)

	ins := ReadInfos(target, f.ID)
	if len(ins) != 1 {
		t.Fatalf("infos: %+v", ins)
	}
	in := ins[0]
	if !in.Mine() || in.Label != "Hollow Knight" || !in.Newest.Equal(newest) || in.Root != paths.Documents || in.Rel != "Hollow Knight" {
		t.Fatalf("info: %+v", in)
	}
	if p, ok := in.Path(); !ok || !strings.EqualFold(p, f.Path) {
		t.Errorf("Path() = %q, %v", p, ok)
	}
	if ReadInfos(target, "../x") != nil || ReadInfos("", f.ID) != nil {
		t.Error("invalid id or target read")
	}

	forgetInfo(target, f.ID)
	if len(ReadInfos(target, f.ID)) != 0 {
		t.Error("own info not forgotten")
	}
	if _, err := os.Stat(filepath.Join(target, InfoDir, f.ID)); err == nil {
		t.Error("empty info folder left behind")
	}
}

func TestReadInfosDistrustsOtherPCs(t *testing.T) {
	target := t.TempDir()
	dir := filepath.Join(target, InfoDir, "game")
	write(t, filepath.Join(dir, "evil.json"), `{"id":"game","label":"Ga\u0007me\u0000","root":"home","rel":"../../Windows","host":"Desk"}`)
	write(t, filepath.Join(dir, "wrong-id.json"), `{"id":"other","label":"Other"}`)
	write(t, filepath.Join(dir, "broken.json"), `{"id":`)
	write(t, filepath.Join(dir, "big.json"), `{"id":"game","label":"`+strings.Repeat("x", maxInfo)+`"}`)
	write(t, filepath.Join(dir, "profile.json"), `{"id":"game","label":"Profile","root":"home","rel":""}`)

	ins := ReadInfos(target, "game")
	if len(ins) != 2 {
		t.Fatalf("want the 2 well-formed files, got %+v", ins)
	}
	for _, in := range ins {
		if in.Mine() {
			t.Errorf("another PC's file counted as mine: %+v", in)
		}
		if p, ok := in.Path(); ok {
			t.Errorf("unsafe location accepted: %q", p)
		}
		if strings.ContainsAny(in.Label, "\x07\x00") {
			t.Errorf("control characters kept: %q", in.Label)
		}
	}
}

func TestRunRecordsBackedAndInfo(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "info-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	write(t, filepath.Join(src, "a.sav"), "aa")
	write(t, filepath.Join(src, "logs", "x.log"), "log")
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(src, "a.sav"), old, old)

	res, err := Run(context.Background(), []Folder{{ID: id, Label: "Game", Path: src, Exclude: []string{"logs"}}},
		Options{Target: target})
	if err != nil || len(res.Backed) != 1 || res.Backed[0] != id {
		t.Fatalf("Run: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(target, id, "logs")); err == nil {
		t.Error("excluded folder was backed up")
	}
	ins := ReadInfos(target, id)
	if len(ins) != 1 || !ins[0].Newest.Equal(old.UTC().Round(time.Second)) {
		t.Fatalf("info after run: %+v (want newest %v: the excluded log doesn't count)", ins, old)
	}
	if b, n := IndexSize(id); b != 2 || n != 1 {
		t.Errorf("IndexSize = %d bytes, %d files", b, n)
	}
}

func TestOrphans(t *testing.T) {
	target := t.TempDir()
	write(t, filepath.Join(target, "mine", "a.sav"), "12345")
	write(t, filepath.Join(target, "gone-game--desk", "b.sav"), "1")
	write(t, filepath.Join(target, VersionsDir, "history-only", "2026-01-02_030405", "c.sav"), "1")
	write(t, filepath.Join(target, InfoDir, "gone-game--desk", "desk.json"),
		`{"id":"gone-game--desk","label":"Gone Game","host":"Desk","backedUp":"2026-09-01T00:00:00Z"}`)
	for _, d := range []string{"bad name!", "empty", filepath.Join(VersionsDir, "pruned")} {
		if err := os.MkdirAll(filepath.Join(target, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := Orphans(context.Background(), target, func(id string) bool { return id == "mine" })
	if len(got) != 2 {
		t.Fatalf("orphans: %+v", got)
	}
	if g := got[0]; g.ID != "gone-game--desk" || g.Label != "Gone Game" || g.Host != "Desk" || g.Bytes != 1 || g.Files != 1 {
		t.Errorf("first: %+v", g)
	}
	if h := got[1]; h.ID != "history-only" || h.Label != "History only" || h.Points != 1 || h.Files != 0 {
		t.Errorf("second: %+v", h)
	}
}

func TestLabelFromID(t *testing.T) {
	for id, want := range map[string]string{"hollow-knight": "Hollow knight", "elden-ring--desk-pc-2": "Elden ring", "x": "X"} {
		if got := LabelFromID(id); got != want {
			t.Errorf("LabelFromID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestNewestAndMatcherExtras(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "save.sav"), "s")
	write(t, filepath.Join(dir, "shots", "1.png"), "p")
	write(t, filepath.Join(dir, ".stignore"), "user.txt\n"+IgnoreBegin+"\nsave.sav\n"+IgnoreEnd+"\n")
	old, newer := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "save.sav"), old, old)
	_ = os.Chtimes(filepath.Join(dir, "shots", "1.png"), newer, newer)

	if got := Newest(dir, nil); !got.Equal(newer) {
		t.Errorf("Newest = %v, want %v", got, newer)
	}
	if got := Newest(dir, []string{"shots"}); !got.Equal(old) {
		t.Errorf("Newest without shots = %v, want %v", got, old)
	}
	m := LoadMatcher(dir, "*.png")
	if !m.Ignored("user.txt") || !m.Ignored("shots/1.png") {
		t.Error("user's line or extra pattern not applied")
	}
	if m.Ignored("save.sav") {
		t.Error("a left-over Syncer block in .stignore was applied")
	}
	if !Newest(filepath.Join(dir, "missing"), nil).IsZero() {
		t.Error("missing folder has a newest time")
	}
}
