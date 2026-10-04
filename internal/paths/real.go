package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Windows reaches one folder by many names: legacy junctions in every
// profile ("Application Data", "Local Settings"), 8.3 short names (SSH~1),
// NTFS stream syntax (Microsoft::$INDEX_ALLOCATION) and links a program made.
// Checks that compare paths as text miss all of them, so the checks below
// also look at the path as the filesystem resolves it.

// realPath returns p as the filesystem has it: the nearest existing
// ancestor's final path (every junction, symlink and short name resolved,
// names as stored on disk), joined with the rest of p that doesn't exist yet.
func realPath(p string) (string, error) {
	p = filepath.Clean(p)
	var tail []string
	for {
		_, err := os.Lstat(p)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
	fp, err := finalPath(p)
	if err == nil {
		fp = unvirtualize(p, fp)
	}
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		fp = filepath.Join(fp, tail[i])
	}
	return fp, nil
}

// finalPath asks Windows where the existing path p really is.
func finalPath(p string) (string, error) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(u, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return dosPath(windows.UTF16ToString(buf[:n])), nil
		}
		buf = make([]uint16, n)
	}
}

// dosPath turns \\?\C:\x into C:\x and \\?\UNC\server\share into
// \\server\share.
func dosPath(s string) string {
	if rest, ok := strings.CutPrefix(s, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(s, `\\?\`)
}

// unvirtualize maps fp, the final path of p, back to p's own spelling when
// all that differs is MSIX AppData redirection: a process started from a
// packaged app (an MSIX launcher, say) can have its writes to AppData go to
// %LOCALAPPDATA%\Packages\<app>\LocalCache\{Local,LocalLow,Roaming}\…, and
// its own folders there aren't aliases. Paths really inside Packages are
// left as they are (and are never syncable anyway).
func unvirtualize(p, fp string) string {
	local := appData()[Local]
	if local == "" {
		return fp
	}
	pkgs := filepath.Join(local, "Packages")
	if _, ok := within(pkgs, p); ok {
		return fp
	}
	rel, ok := within(pkgs, fp)
	if !ok {
		return fp
	}
	parts := strings.SplitN(rel, `\`, 4)
	if len(parts) < 3 || !strings.EqualFold(parts[1], "LocalCache") {
		return fp
	}
	for _, root := range []string{Local, LocalLow, Roaming} {
		base := appData()[root]
		if base == "" || !strings.EqualFold(parts[2], filepath.Base(base)) {
			continue
		}
		if len(parts) == 4 {
			return filepath.Join(base, parts[3])
		}
		return base
	}
	return fp
}

// appData is where this user's AppData folders are, as Windows says: unlike
// roots, tests don't move them.
var appData = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	for _, name := range []string{Local, LocalLow, Roaming} {
		if p, err := windows.KnownFolderPath(known[name], 0); err == nil && p != "" {
			m[name] = filepath.Clean(p)
		}
	}
	return m
})

// linkBelow reports whether an existing folder or file on the way from base
// (excluded) down to p is a junction or symbolic link (a name-surrogate
// reparse point). Other reparse points, like OneDrive's cloud files, stay
// where they are and are fine.
func linkBelow(base, p string) bool {
	rel, ok := within(filepath.Clean(base), filepath.Clean(p))
	if !ok {
		return true
	}
	if rel == "." {
		return false
	}
	cur := filepath.Clean(base)
	for _, seg := range strings.Split(rel, `\`) {
		cur = filepath.Join(cur, seg)
		u, err := windows.UTF16PtrFromString(cur)
		if err != nil {
			return true
		}
		a, err := windows.GetFileAttributes(u)
		if err != nil {
			return false // doesn't exist (yet): nothing further down does either
		}
		if a&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			continue
		}
		var fd windows.Win32finddata
		h, err := windows.FindFirstFile(u, &fd)
		if err != nil {
			return true
		}
		windows.FindClose(h)
		if fd.Reserved0&0x20000000 != 0 { // IsReparseTagNameSurrogate
			return true
		}
	}
	return false
}

// hasStream reports whether p names an NTFS stream or uses a colon anywhere
// past its drive (C:\a\b:stream, Microsoft::$INDEX_ALLOCATION).
func hasStream(p string) bool {
	return strings.Contains(p[len(filepath.VolumeName(p)):], ":")
}

// badSegment reports whether a relative path segment from another PC could
// name something else than it says: a stream (":") or a short name ("~").
func badSegment(seg string) bool {
	return strings.ContainsAny(seg, ":~")
}
