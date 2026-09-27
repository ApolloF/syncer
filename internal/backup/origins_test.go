package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPointOrigins(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "origin-" + time.Now().Format("150405.000000")
	defer os.Remove(indexPath(id))
	f := Folder{ID: id, Label: "Test", Path: src}
	defer func(o func(string, string) string) { FileOrigin = o }(FileOrigin)
	from := "Laptop"
	FileOrigin = func(string, string) string { return from }

	write(t, filepath.Join(src, "slot1.sav"), "v1")
	if _, err := Run(context.Background(), []Folder{f}, Options{Target: target}); err != nil {
		t.Fatal(err)
	}
	if _, latest := PointOrigins(target, id); len(latest.From) != 1 || latest.From[0] != "Laptop" {
		t.Fatalf("latest = %+v, want from Laptop", latest)
	}

	// Desktop changes the save; the next run keeps Laptop's version as a point.
	time.Sleep(1100 * time.Millisecond) // a new stamp
	from = "Desktop"
	write(t, filepath.Join(src, "slot1.sav"), "v2")
	_ = os.Chtimes(filepath.Join(src, "slot1.sav"), time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if _, err := Run(context.Background(), []Folder{f}, Options{Target: target}); err != nil {
		t.Fatal(err)
	}
	pts, latest := PointOrigins(target, id)
	if len(latest.From) != 1 || latest.From[0] != "Desktop" {
		t.Errorf("latest = %+v, want from Desktop", latest)
	}
	all := Points(target, id)
	if len(all) != 1 {
		t.Fatalf("points = %v, want 1", all)
	}
	o := pts[all[0]]
	if len(o.From) != 1 || o.From[0] != "Laptop" || len(o.By) != 1 || o.By[0] != hostName {
		t.Errorf("point origin = %+v, want from Laptop by %s", o, hostName)
	}
}
