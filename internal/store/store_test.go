package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsSurviveOlderFiles(t *testing.T) {
	s := defaults()
	if err := json.Unmarshal([]byte(`{"theme":"dark","keepDays":7}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.PauseWhileGaming || s.Theme != "dark" || s.KeepDays != 7 || s.BackupOnly == nil {
		t.Errorf("got %+v", s)
	}
}

func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	for _, v := range []int{1, 2} {
		if err := WriteJSON(p, map[string]int{"v": v}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]int
	if err := json.Unmarshal(b, &m); err != nil || m["v"] != 2 {
		t.Errorf("got %s, %v", b, err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("temp files left behind: %d entries", len(es))
	}
}
