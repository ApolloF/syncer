package backup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/winx"
)

// DriveInfo describes where Google Drive for desktop exposes "My Drive".
type DriveInfo struct {
	Found   bool   `json:"found"`
	Running bool   `json:"running"`
	MyDrive string `json:"myDrive"` // e.g. G:\My Drive
}

// DetectDrive finds Google Drive for desktop's "My Drive" folder, both for the
// default streaming mode (virtual drive letter) and mirror mode (a folder in
// the user profile). The name "My Drive" is localized, so the virtual drive is
// recognised by its fixed siblings instead.
func DetectDrive() DriveInfo {
	info := DriveInfo{Running: driveRunning()}
	for l := 'D'; l <= 'Z'; l++ {
		root := string(l) + `:\`
		if !isDir(filepath.Join(root, ".shortcut-targets-by-id")) {
			continue
		}
		if md := myDriveIn(root); md != "" {
			info.Found, info.MyDrive = true, md
			return info
		}
	}
	home := paths.Root(paths.Home)
	for _, n := range []string{"My Drive", "Google Drive"} {
		if p := filepath.Join(home, n); isDir(p) {
			info.Found, info.MyDrive = true, p
			return info
		}
	}
	return info
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

// Target returns the backup root folder (custom override or <My Drive>\GameSaveBackup).
func Target(override string) (string, bool) {
	if override != "" {
		return override, isDir(filepath.Dir(filepath.Clean(override))) || isDir(override)
	}
	d := DetectDrive()
	if !d.Found {
		return "", false
	}
	return filepath.Join(d.MyDrive, "GameSaveBackup"), true
}
