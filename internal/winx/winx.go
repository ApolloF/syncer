// Package winx provides Windows process and shell helpers.
package winx

import (
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	setThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")
	queryNotification = windows.NewLazySystemDLL("shell32.dll").NewProc("SHQueryUserNotificationState")
)

// BackgroundProcess lowers CPU, IO and memory priority for the current process.
func BackgroundProcess() error {
	return windows.SetPriorityClass(windows.CurrentProcess(), 0x00100000)
}

// BackgroundThread lowers the current thread's priority until end is called.
// Call end on the same goroutine, normally with defer.
func BackgroundThread() (end func()) {
	runtime.LockOSThread()
	if err := setThreadPriority.Find(); err != nil {
		runtime.UnlockOSThread()
		return func() {}
	}
	ok, _, _ := setThreadPriority.Call(uintptr(windows.CurrentThread()), 0x00010000)
	if ok == 0 {
		runtime.UnlockOSThread()
		return func() {}
	}
	ended := false
	return func() {
		if ended {
			return
		}
		ended = true
		setThreadPriority.Call(uintptr(windows.CurrentThread()), 0x00020000)
		runtime.UnlockOSThread()
	}
}

// FullScreen reports busy, Direct3D full-screen or presentation notification state.
func FullScreen() bool {
	if err := queryNotification.Find(); err != nil {
		return false
	}
	var state uint32
	hr, _, _ := queryNotification.Call(uintptr(unsafe.Pointer(&state)))
	return int32(hr) >= 0 && state >= 2 && state <= 4
}

// ForegroundPath returns the executable path of the process that owns the
// foreground window, or "". Only the foreground app counts as "playing":
// store-installed tools like Wallpaper Engine run all day in the background.
func ForegroundPath() string {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return ""
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// System32 resolves a filename in the system directory, or returns "" on failure.
func System32(name string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return ""
	}
	return systemFile(dir, name)
}

// WindowsDir resolves a filename in the Windows directory, or returns "" on failure.
func WindowsDir(name string) string {
	dir, err := windows.GetWindowsDirectory()
	if err != nil {
		return ""
	}
	return systemFile(dir, name)
}

func systemFile(dir, name string) string {
	if !filepath.IsAbs(dir) || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`+"\x00") {
		return ""
	}
	return filepath.Join(dir, name)
}
