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

// OneDriveTwin returns another copy of a Documents save folder, left on the
// other side of OneDrive: OneDrive\Documents\<game> when this PC keeps
// Documents locally (a PC that once had Documents in OneDrive, or another PC
// that does, left it there), or the profile's own Documents\<game> when
// Documents was moved into OneDrive. "" when there is none.
func OneDriveTwin(p string) string {
	docs := Root(Documents)
	if docs == "" {
		return ""
	}
	od := OneDriveRoots()
	for _, c := range twinCandidates(docs, WithinAny(od, docs), od, Root(Home), p) {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// twinCandidates lists where another copy of p may be (see OneDriveTwin).
func twinCandidates(docs string, docsInOneDrive bool, oneDrive []string, home, p string) []string {
	rel, ok := within(filepath.Clean(docs), filepath.Clean(p))
	if !ok || rel == "." {
		return nil
	}
	var out []string
	add := func(c string) {
		c = filepath.Clean(c)
		if !strings.EqualFold(c, filepath.Clean(p)) {
			out = append(out, c)
		}
	}
	if docsInOneDrive {
		if home != "" {
			add(filepath.Join(home, "Documents", rel))
		}
		return out
	}
	for _, r := range oneDrive {
		add(filepath.Join(r, "Documents", rel))
		if b := filepath.Base(docs); !strings.EqualFold(b, "Documents") {
			add(filepath.Join(r, b, rel))
		}
	}
	return out
}
