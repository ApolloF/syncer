package paths

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

const oneDriveAccounts = `Software\Microsoft\OneDrive\Accounts`

var (
	oneDriveMu   sync.Mutex
	oneDriveAt   time.Time
	oneDriveDirs []string
)

// OneDriveRoots lists the folders OneDrive syncs on this PC: the folder of
// every signed-in account (personal and work or school). A moved Documents
// folder lies inside one of them. Cached for a minute: it's asked per folder.
func OneDriveRoots() []string {
	oneDriveMu.Lock()
	defer oneDriveMu.Unlock()
	if time.Since(oneDriveAt) > time.Minute {
		oneDriveDirs, oneDriveAt = readOneDriveRoots(), time.Now()
	}
	return oneDriveDirs
}

// readOneDriveRoots reads each account's UserFolder. The OneDrive environment
// variables aren't used: they stay behind after an account is unlinked.
func readOneDriveRoots() []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, oneDriveAccounts, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		ak, err := registry.OpenKey(registry.CURRENT_USER, oneDriveAccounts+`\`+n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		dir, _, err := ak.GetStringValue("UserFolder")
		ak.Close()
		if err != nil || dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || seen[strings.ToLower(dir)] {
			continue
		}
		seen[strings.ToLower(dir)] = true
		out = append(out, dir)
	}
	return out
}

// WithinAny reports whether p is one of roots or lies inside one.
func WithinAny(roots []string, p string) bool {
	for _, r := range roots {
		if Within(r, p) {
			return true
		}
	}
	return false
}

// InOneDrive reports whether OneDrive syncs p on this PC.
func InOneDrive(p string) bool { return WithinAny(OneDriveRoots(), p) }
