// Package winx provides Windows process and shell helpers.
package winx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	setThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")
	queryNotification = windows.NewLazySystemDLL("shell32.dll").NewProc("SHQueryUserNotificationState")
	shFileOperation   = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")
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

// shFileOpStruct is SHFILEOPSTRUCTW as laid out on 64-bit Windows.
type shFileOpStruct struct {
	hwnd                  windows.HWND
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete           = 0x0003
	fofAllowUndo       = 0x0040
	fofWantNukeWarning = 0x4000
)

// ErrCancelled means the user cancelled in one of Windows' own dialogs.
var ErrCancelled = errors.New("cancelled")

// Recycle moves a folder (or file) and everything in it to the Recycle Bin,
// with Windows' usual confirmation and progress. Windows asks again before
// anything would be deleted for good instead (too big for the Recycle Bin, or
// a drive without one). Its dialogs belong to this app's foreground window.
func Recycle(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("not a full path")
	}
	if err := shFileOperation.Find(); err != nil {
		return err
	}
	from, err := windows.UTF16FromString(filepath.Clean(path))
	if err != nil {
		return err
	}
	from = append(from, 0) // a list of paths, ended by an empty one

	// The shell wants COM on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || err == syscall.Errno(1) { // S_FALSE: already initialized
		defer windows.CoUninitialize()
	}
	op := shFileOpStruct{hwnd: ownWindow(), wFunc: foDelete, pFrom: &from[0], fFlags: fofAllowUndo | fofWantNukeWarning}
	r, _, _ := shFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(from)
	if op.fAnyOperationsAborted != 0 {
		return ErrCancelled
	}
	if r != 0 {
		return fmt.Errorf("Windows error 0x%x", r)
	}
	return nil
}

// ownWindow returns the foreground window if it belongs to this process.
func ownWindow() windows.HWND {
	h := windows.GetForegroundWindow()
	var pid uint32
	if h == 0 {
		return 0
	}
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil || int(pid) != os.Getpid() {
		return 0
	}
	return h
}
