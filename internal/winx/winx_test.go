package winx

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSystemPaths(t *testing.T) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	win, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if got := System32("cmd.exe"); got != filepath.Join(system, "cmd.exe") || !filepath.IsAbs(got) {
		t.Errorf("System32: %q", got)
	}
	if got := WindowsDir("explorer.exe"); got != filepath.Join(win, "explorer.exe") || !filepath.IsAbs(got) {
		t.Errorf("WindowsDir: %q", got)
	}
	for _, name := range []string{"", ".", "..", `..\evil.exe`, `C:\evil.exe`, `sub/tool.exe`, "cmd.exe:stream", "bad\x00.exe"} {
		if System32(name) != "" || WindowsDir(name) != "" {
			t.Errorf("accepted invalid filename %q", name)
		}
	}
}

func TestBackgroundThread(t *testing.T) {
	end := BackgroundThread()
	defer end()
	end()
}
