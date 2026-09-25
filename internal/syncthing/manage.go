package syncthing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

const createNoWindow = 0x08000000

// FindExe locates syncthing.exe (WinGet link, WinGet package dir, or PATH).
func FindExe() string {
	local := paths.Root(paths.Local)
	cands := []string{filepath.Join(local, "Microsoft", "WinGet", "Links", "syncthing.exe")}
	if m, _ := filepath.Glob(filepath.Join(local, "Microsoft", "WinGet", "Packages", "Syncthing.Syncthing_*", "syncthing-*", "syncthing.exe")); len(m) > 0 {
		sort.Sort(sort.Reverse(sort.StringSlice(m))) // newest version first
		cands = append(cands, m...)
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("syncthing.exe"); err == nil {
		return p
	}
	return ""
}

// Hidden returns a command that runs without flashing a console window.
func Hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// HiddenContext is Hidden for a command that must be cancelable, e.g. a
// winget install/uninstall the user can abort.
func HiddenContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// Start launches Syncthing in the background and waits for its API.
func Start(ctx context.Context) error {
	exe := FindExe()
	if exe == "" {
		return errors.New("syncthing.exe not found")
	}
	cmd := Hidden(exe, "--no-browser", "--no-restart")
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return WaitReady(ctx, 30*time.Second)
}

// WaitReady polls until the API answers or timeout passes.
func WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := New(); err == nil {
			if _, err := c.Status(ctx); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return ErrNotRunning
}

// winget returns winget's own app alias under %LOCALAPPDATA%, so a program
// named winget earlier on PATH isn't run instead; PATH only as a fallback.
func winget() string {
	p := filepath.Join(paths.Root(paths.Local), "Microsoft", "WindowsApps", "winget.exe")
	if _, err := os.Lstat(p); err == nil {
		return p
	}
	return "winget"
}

// Install installs Syncthing via winget (per-user, silent).
func Install(ctx context.Context) error {
	cmd := HiddenContext(ctx, winget(), "install", "--id", "Syncthing.Syncthing", "-e", "--silent",
		"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
	out, err := cmd.CombinedOutput()
	if err != nil && FindExe() == "" {
		return errors.New("winget install failed: " + lastLine(string(out)))
	}
	return nil
}

// Uninstall removes Syncthing via winget (per-user, silent).
func Uninstall(ctx context.Context) error {
	cmd := HiddenContext(ctx, winget(), "uninstall", "--id", "Syncthing.Syncthing", "-e", "--silent",
		"--disable-interactivity", "--accept-source-agreements")
	out, err := cmd.CombinedOutput()
	if err != nil && FindExe() != "" {
		return errors.New("winget uninstall failed: " + lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
