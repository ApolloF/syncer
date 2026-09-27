package accounts

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/paths"
)

// ---- where things go -----------------------------------------------------------

// localRoot is %LOCALAPPDATA% (a var for tests).
var localRoot = func() string { return paths.Root(paths.Local) }

// VaultName is the folder holding the saves of the accounts that aren't active.
const VaultName = "SyncerAccounts"

// vaultBase is the vault on the same drive as p, so switching account is a
// rename there: %LOCALAPPDATA%\SyncerAccounts on the system drive, otherwise
// <drive>\SyncerAccounts.
func vaultBase(p string) string {
	l := localRoot()
	vol := filepath.VolumeName(p)
	if vol == "" || strings.EqualFold(vol, filepath.VolumeName(l)) {
		return filepath.Join(l, VaultName)
	}
	return vol + `\` + VaultName
}

// VaultDir is where an account's saves of a game wait while another account
// is active on this PC. The folder next to the saves is preferred; when that
// drive can't hold one, %LOCALAPPDATA% is used (switching then copies).
func VaultDir(live, account, game string) string {
	base := vaultBase(live)
	if err := os.MkdirAll(base, 0o755); err != nil {
		base = filepath.Join(localRoot(), VaultName)
	}
	return filepath.Join(base, account, game)
}

// InVault reports whether p lies in one of the vaults.
func InVault(p string) bool {
	return paths.Within(vaultBase(p), p) || paths.Within(filepath.Join(localRoot(), VaultName), p)
}

// Retire moves a folder into the vault's .trash (see retire).
func Retire(p string) (string, error) { return retire(p) }

// retire moves p out of the way into the vault's .trash on the same drive
// (never deleting anything). It returns where it went.
func retire(p string) (string, error) {
	base := filepath.Join(vaultBase(p), ".trash", time.Now().Format("2006-01-02_150405.000000000"))
	dst := filepath.Join(base, filepath.Base(p))
	if err := os.MkdirAll(base, 0o755); err == nil {
		if err := os.Rename(p, dst); err == nil {
			return dst, nil
		}
	}
	// The drive has no room for a .trash: keep it next to where it was.
	dst = p + ".syncer-old-" + time.Now().Format("20060102-150405")
	if err := os.Rename(p, dst); err != nil {
		return "", fmt.Errorf("could not move %s out of the way: %w", p, err)
	}
	return dst, nil
}

// ---- trees ---------------------------------------------------------------------

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// splitSkip leaves Syncthing's own folders, conflict copies and unfinished
// copies out of an account's new saves.
func splitSkip(rel string, d fs.DirEntry) bool {
	top := strings.ToLower(strings.SplitN(filepath.ToSlash(rel), "/", 2)[0])
	if top == ".stfolder" || top == ".stversions" {
		return true
	}
	if d.IsDir() {
		return false
	}
	if strings.HasSuffix(d.Name(), ".syncer-tmp") {
		return true
	}
	_, _, ok := conflict.Parse(d.Name())
	return ok
}

// copyTree copies the directory src to dst (which must not exist yet), every
// file checked byte for byte against its source. skip may leave entries out.
func copyTree(src, dst string, skip func(rel string, d fs.DirEntry) bool) error {
	if exists(dst) {
		return fmt.Errorf("%s already exists", dst)
	}
	if !isDir(src) {
		return fmt.Errorf("%s is not a folder", src)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // an unreadable file must not silently go missing
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if rel == "." {
			return os.MkdirAll(out, 0o755)
		}
		if skip != nil && skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type().IsRegular():
			return copyVerified(p, out)
		default:
			return nil // links and devices aren't saves; Syncthing doesn't follow them either
		}
	})
}

// copyVerified copies one file, keeps its modified time and checks the copy
// against the source. dst is replaced only once the copy is complete.
func copyVerified(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".syncer-tmp"
	sum, err := copyHash(src, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	got, err := hashFile(tmp)
	if err != nil || !bytes.Equal(got, sum) {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy of %s did not verify", src)
	}
	_ = os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func copyHash(src, dst string) ([]byte, error) {
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return nil, err
	}
	return h.Sum(nil), out.Close()
}

func hashFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// sameTree reports whether two trees hold the same files with the same bytes.
func sameTree(a, b string) (bool, error) {
	files := func(root string) (map[string]string, error) {
		m := map[string]string{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			m[strings.ToLower(rel)] = p
			return nil
		})
		return m, err
	}
	fa, err := files(a)
	if err != nil {
		return false, err
	}
	fb, err := files(b)
	if err != nil {
		return false, err
	}
	if len(fa) != len(fb) {
		return false, nil
	}
	for rel, pa := range fa {
		pb, ok := fb[rel]
		if !ok {
			return false, nil
		}
		ha, err := hashFile(pa)
		if err != nil {
			return false, err
		}
		hb, err := hashFile(pb)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(ha, hb) {
			return false, nil
		}
	}
	return true, nil
}

// moveDir moves the folder src to dst (which must not exist). On the same
// drive that's one rename. Across drives the whole tree is copied, checked,
// put in place, and only then src goes to the .trash (not deleted).
func moveDir(src, dst string) error {
	if exists(dst) {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if strings.EqualFold(filepath.VolumeName(src), filepath.VolumeName(dst)) {
		return err // same drive: a real failure (e.g. a file is open), not a cross-drive move
	}
	tmp := dst + ".syncer-move"
	if exists(tmp) {
		if _, err := retire(tmp); err != nil {
			return err
		}
	}
	if err := copyTree(src, tmp, nil); err != nil {
		_, _ = retire(tmp)
		return err
	}
	if ok, err := sameTree(src, tmp); err != nil || !ok {
		_, _ = retire(tmp)
		return errors.New("the copied saves did not match the originals; nothing was moved")
	}
	if err := os.Rename(tmp, dst); err != nil {
		_, _ = retire(tmp)
		return err
	}
	if _, err := retire(src); err != nil {
		// Both copies exist now. Take the new one back out so the next try
		// starts from the original again.
		_, _ = retire(dst)
		return err
	}
	return nil
}

// OpenFiles lists files in dir that another program has open (a game still
// running), found by trying to open each one exclusively. Only the first few
// are returned.
func OpenFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if !exclusiveOpen(p) {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
			if len(out) >= 5 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return out
}

func exclusiveOpen(p string) bool {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return true
	}
	h, err := windows.CreateFile(u, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		// Sharing violations mean another program has it open; anything
		// else (e.g. access denied) isn't this check's business.
		return !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION)
	}
	windows.CloseHandle(h)
	return true
}
