package backup

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/winx"
)

// DriveInfo describes where Google Drive for desktop exposes "My Drive".
type DriveInfo struct {
	Found   bool           `json:"found"`
	Running bool           `json:"running"`
	MyDrive string         `json:"myDrive"` // the one backups go to, e.g. G:\My Drive
	Drives  []DriveAccount `json:"drives"`  // every "My Drive" found (one per signed-in account)
}

// DriveAccount is one signed-in account's "My Drive" folder.
type DriveAccount struct {
	MyDrive string `json:"myDrive"`
	Label   string `json:"label"` // volume label when it says more than "Google Drive"
}

// DetectDrive finds Google Drive for desktop's "My Drive" folders. preferred
// is the user's pick when several accounts are signed in; if it isn't mounted
// right now the result is not found rather than silently using another
// account's Drive. With no preference the first one found is used.
func DetectDrive(preferred string) DriveInfo {
	info := DriveInfo{Running: driveRunning(), Drives: DetectDrives()}
	if preferred != "" {
		info.MyDrive = preferred
		for _, d := range info.Drives {
			if strings.EqualFold(filepath.Clean(d.MyDrive), filepath.Clean(preferred)) {
				info.Found, info.MyDrive = true, d.MyDrive
			}
		}
		return info
	}
	if len(info.Drives) > 0 {
		info.Found, info.MyDrive = true, info.Drives[0].MyDrive
	}
	return info
}

// DetectDrives lists "My Drive" for every account, both for the default
// streaming mode (one virtual drive letter per account) and mirror mode (a
// folder in the user profile). The name "My Drive" is localized, so the
// virtual drive is recognised by its fixed siblings instead.
func DetectDrives() []DriveAccount {
	var out []DriveAccount
	for l := 'D'; l <= 'Z'; l++ {
		root := string(l) + `:\`
		if !isDir(filepath.Join(root, ".shortcut-targets-by-id")) {
			continue
		}
		if md := myDriveIn(root); md != "" {
			out = append(out, DriveAccount{MyDrive: md, Label: volumeLabel(root)})
		}
	}
	home := paths.Root(paths.Home)
	es, _ := os.ReadDir(home)
	for _, e := range es {
		n := strings.ToLower(e.Name())
		// "My Drive", "Google Drive", and "My Drive (2)" etc. for extra accounts.
		if e.IsDir() && (strings.HasPrefix(n, "my drive") || strings.HasPrefix(n, "google drive")) {
			out = append(out, DriveAccount{MyDrive: filepath.Join(home, e.Name())})
		}
	}
	return out
}

func volumeLabel(root string) string {
	r, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumeInformation(r, &buf[0], uint32(len(buf)), nil, nil, nil, nil, 0) != nil {
		return ""
	}
	l := strings.TrimSpace(windows.UTF16ToString(buf))
	if strings.EqualFold(l, "Google Drive") {
		return ""
	}
	return l
}

func myDriveIn(root string) string {
	if p := filepath.Join(root, "My Drive"); isDir(p) {
		return p
	}
	skip := map[string]bool{"other computers": true, "shared drives": true, ".shortcut-targets-by-id": true,
		"$recycle.bin": true, "system volume information": true}
	es, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range es {
		n := strings.ToLower(e.Name())
		// Localized siblings ("Gedeelde drives", "Geteilte Ablagen", …) can't be
		// listed exhaustively; "My Drive" localizations contain "drive"/"ablage"
		// far less reliably, so pick the first unknown, non-hidden directory.
		if e.IsDir() && !skip[n] && !strings.HasPrefix(n, ".") && !strings.HasPrefix(n, "$") &&
			!strings.Contains(n, "shared") && !strings.Contains(n, "gedeeld") && !strings.Contains(n, "geteilt") &&
			!strings.Contains(n, "other") && !strings.Contains(n, "andere") {
			return filepath.Join(root, e.Name())
		}
	}
	return ""
}

func driveRunning() bool {
	out, err := hiddenOutput(winx.System32("tasklist.exe"), "/FI", "IMAGENAME eq GoogleDriveFS.exe", "/NH")
	return err == nil && strings.Contains(strings.ToLower(out), "googledrivefs.exe")
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Target returns the backup root folder (custom override or
// <My Drive>\GameSaveBackup of the preferred Google account).
func Target(override, preferredDrive string) (string, bool) {
	if override != "" {
		return override, isDir(filepath.Dir(filepath.Clean(override))) || isDir(override)
	}
	d := DetectDrive(preferredDrive)
	if !d.Found {
		return "", false
	}
	return filepath.Join(d.MyDrive, "GameSaveBackup"), true
}
