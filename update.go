package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/update"
)

// UpdateInfo is a newer Syncer release.
type UpdateInfo struct {
	Latest string `json:"latest"`
	URL    string `json:"url"`
}

const updateEvery = 24 * time.Hour

// updateLoop looks for a new release a minute after start, then daily, and
// installs it by itself when nobody is looking (see autoUpdateNow).
func (a *App) updateLoop(ctx context.Context) {
	if version == "dev" {
		return
	}
	if exe, ok := taskExe(); ok {
		update.Cleanup(exe)
	}
	sleep(ctx, time.Minute)
	for ctx.Err() == nil {
		if u := store.LoadState().Update; u == nil || time.Since(u.Checked) > updateEvery {
			if _, err := a.checkUpdate(ctx); err != nil {
				logx.Printf("update check: %v", err)
			}
		}
		if availableUpdate() != nil && a.autoUpdateNow() {
			if err := a.installUpdate(ctx, true); err != nil {
				logx.Printf("update: %v", err)
			}
		}
		sleep(ctx, time.Hour)
	}
}

// autoUpdateNow reports whether Syncer may update and restart by itself
// right now: it's on, only the tray icon shows (so no window closes under
// the user), no game runs and no backup is in progress.
func (a *App) autoUpdateNow() bool {
	if store.LoadSettings().NoAutoUpdate {
		return false
	}
	a.mu.Lock()
	ok := a.hidden && !a.backingUp && !a.updating
	a.mu.Unlock()
	return ok && !a.gameRunning()
}

// checkUpdate asks GitHub for the newest release and remembers it.
func (a *App) checkUpdate(ctx context.Context) (*UpdateInfo, error) {
	if version == "dev" {
		return nil, errors.New("this is a development build")
	}
	if store.LoadSettings().NoUpdateCheck {
		return nil, nil
	}
	r, err := update.Latest(ctx)
	if err != nil {
		return nil, err
	}
	store.UpdateState(func(st *store.State) {
		st.Update = &store.Update{Checked: time.Now(), Latest: r.Tag, URL: r.URL}
	})
	u := availableUpdate()
	if u != nil {
		logx.Printf("Syncer %s is available (this is %s)", u.Latest, version)
		a.refreshTray()
		runtime.EventsEmit(a.ctx, "changed")
	}
	return u, nil
}

// CheckForUpdate checks now (Settings); nil means this is the newest version.
func (a *App) CheckForUpdate() (*UpdateInfo, error) {
	ctx, cancel := a.callCtx()
	defer cancel()
	return a.checkUpdate(ctx)
}

// availableUpdate is the newer release found by the last check, if any.
func availableUpdate() *UpdateInfo {
	u := store.LoadState().Update
	if u == nil || store.LoadSettings().NoUpdateCheck || !update.Newer(u.Latest, version) ||
		!strings.HasPrefix(u.URL, update.ReleasesURL) {
		return nil
	}
	return &UpdateInfo{Latest: u.Latest, URL: u.URL}
}

// OpenUpdate opens the page of the newer release.
func (a *App) OpenUpdate() {
	if u := availableUpdate(); u != nil {
		runtime.BrowserOpenURL(a.ctx, u.URL)
	}
}

// InstallUpdate downloads the newest release, puts it in place of this exe
// and restarts Syncer with it.
func (a *App) InstallUpdate() error {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	return a.installUpdate(ctx, false)
}

var errBackupRunning = errors.New("a backup is running; update once it has finished")

// installUpdate replaces Syncer.exe with the newest release's and restarts.
// Installed and portable copies both update this way (the installer does
// no more than put Syncer.exe in place either). With auto, it gives up
// quietly when the moment passed while it downloaded.
func (a *App) installUpdate(ctx context.Context, auto bool) error {
	exe, ok := taskExe()
	if !ok || version == "dev" {
		return errors.New("development builds don't update themselves")
	}
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return errors.New("Syncer is already updating")
	}
	if a.backingUp {
		a.mu.Unlock()
		return errBackupRunning
	}
	a.updating = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
	}()

	r, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(r.Tag, version) {
		return errors.New("this is already the newest version")
	}
	if r.Exe == nil {
		return errors.New("Syncer " + r.Tag + " can't be installed from here; download it from GitHub")
	}
	path, err := update.Download(ctx, r.Exe, filepath.Dir(exe))
	if err != nil {
		return fmt.Errorf("download Syncer %s: %w", r.Tag, err)
	}
	a.mu.Lock()
	busy, hidden := a.backingUp, a.hidden
	a.mu.Unlock()
	if busy || (auto && (!hidden || a.gameRunning())) {
		_ = os.Remove(path)
		if auto {
			return nil // next time
		}
		return errBackupRunning
	}
	if err := update.Replace(exe, path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("install Syncer %s: %w", r.Tag, err)
	}
	logx.Printf("updated to Syncer %s (from %s), restarting", r.Tag, version)
	setInstalledVersion(exe, r.Tag)

	args := []string{afterUpdateArg + strconv.Itoa(os.Getpid())}
	if hidden {
		args = append(args, "--tray")
	}
	if err := exec.Command(exe, args...).Start(); err != nil {
		return fmt.Errorf("Syncer %s is installed but didn't start (%w); quit Syncer and open it again", r.Tag, err)
	}
	a.quit()
	return nil
}

// afterUpdateArg starts the new exe. It names the process it replaces, which
// holds the single-instance lock until it has exited.
const afterUpdateArg = "--after-update="

// waitForUpdated waits (a while) for the Syncer that started this one to exit.
func waitForUpdated(arg string) {
	pid, err := strconv.ParseUint(strings.TrimPrefix(arg, afterUpdateArg), 10, 32)
	if err != nil {
		return
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, 30_000)
}

// setInstalledVersion keeps the version "Installed apps" shows right after
// an installed Syncer updated itself.
func setInstalledVersion(exe, tag string) {
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "uninstall.exe")); err != nil {
		return // portable
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\ApolloFSyncer`,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if icon, _, err := k.GetStringValue("DisplayIcon"); err != nil || !strings.EqualFold(filepath.Clean(icon), filepath.Clean(exe)) {
		return // installed somewhere else
	}
	v := strings.TrimPrefix(tag, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	_ = k.SetStringValue("DisplayVersion", v)
}
