package gdrive

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ApolloF/syncer/internal/paths"
)

// The token is the only secret Syncer keeps. It's stored encrypted with
// Windows' data protection (DPAPI) for this Windows account, so another
// account on the PC, or a copy of the file on another PC, can't use it.

// tokenFile is where the token is kept; a variable for tests.
var tokenFile = func() string { return filepath.Join(paths.AppDir(), "gdrive.token") }

// LoadToken reads the stored token. ErrSignedOut when there's none.
func LoadToken() (Token, error) {
	b, err := os.ReadFile(tokenFile())
	if errors.Is(err, fs.ErrNotExist) {
		return Token{}, ErrSignedOut
	}
	if err != nil {
		return Token{}, err
	}
	plain, err := unprotect(b)
	if err != nil {
		return Token{}, ErrSignedOut // another account's or PC's: sign in again
	}
	var t Token
	if err := json.Unmarshal(plain, &t); err != nil || t.Refresh == "" {
		return Token{}, ErrSignedOut
	}
	return t, nil
}

// SaveToken stores the token.
func SaveToken(t Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	enc, err := protect(b)
	if err != nil {
		return err
	}
	p := tokenFile()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DeleteToken forgets the token (signing out on this PC).
func DeleteToken() error {
	if err := os.Remove(tokenFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SignedIn reports whether a token is stored.
func SignedIn() bool {
	_, err := LoadToken()
	return err == nil
}

// HasToken is SignedIn without reading the token: cheap enough for every
// lookup of the backup folder.
func HasToken() bool {
	fi, err := os.Stat(tokenFile())
	return err == nil && fi.Size() > 0
}

var entropy = []byte("Syncer Google Drive token")

func protect(plain []byte) ([]byte, error) {
	in := blob(plain)
	ent := blob(entropy)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(enc []byte) ([]byte, error) {
	in := blob(enc)
	ent := blob(entropy)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func blob(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}
