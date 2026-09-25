package main

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/energye/systray"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows/registry"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
)

//go:embed build/tray.ico
var trayIcon []byte

// startTray shows Syncer's icon in the notification area for as long as the
// app runs. Left click opens the window; right click shows the menu.
func (a *App) startTray() {
	go func() {
		// The tray's hidden window and its message loop must share a thread.
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTooltip("Syncer")
			systray.SetOnClick(func(systray.IMenu) { a.showWindow() })
			systray.SetOnDClick(func(systray.IMenu) { a.showWindow() })
			systray.AddMenuItem("Open Syncer", "").Click(a.showWindow)
			systray.AddMenuItem("Back up now", "").Click(func() {
				if err := a.BackupNow(); err != nil && err != backup.ErrBusy {
					logx.Printf("tray backup: %v", err)
				}
			})
			systray.AddSeparator()
			systray.AddMenuItem("Quit Syncer", "").Click(a.quit)
		}, nil)
	}()
}

func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
	wruntime.WindowSetAlwaysOnTop(a.ctx, true) // bring to front…
	wruntime.WindowSetAlwaysOnTop(a.ctx, false)
}

// quit really exits, even with "keep running in the tray" on.
func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	wruntime.Quit(a.ctx)
}

// beforeClose hides the window to the tray instead of exiting when the user
// chose to keep Syncer running.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()
	if quitting || !store.LoadSettings().CloseToTray {
		return false
	}
	wruntime.WindowHide(ctx)
	return true
}

func (a *App) shutdown(context.Context) { systray.Quit() }

// ---- start with Windows ------------------------------------------------------

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "Syncer"

// ensureAutostart makes the per-user Run entry match the setting and point at
// the current exe. Syncer then starts hidden in the tray at sign-in.
func ensureAutostart(on bool) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if strings.HasSuffix(strings.ToLower(exe), "-dev.exe") {
		return // `wails dev` binary
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		logx.Printf("autostart: %v", err)
		return
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			logx.Printf("autostart: %v", err)
		}
		return
	}
	want := `"` + exe + `" --tray`
	if cur, _, err := k.GetStringValue(runValue); err == nil && cur == want {
		return
	}
	if err := k.SetStringValue(runValue, want); err != nil {
		logx.Printf("autostart: %v", err)
	}
}

// removeAutostart is used by the uninstaller.
func removeAutostart() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(runValue)
		k.Close()
	}
}
