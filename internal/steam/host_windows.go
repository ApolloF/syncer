package steam

import (
	"os"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const steamKey = `Software\Valve\Steam`

// hostInfo reads what Steam keeps in the registry.
func hostInfo() Host {
	h := Host{Running: running, Signed: signed}
	if k, err := registry.OpenKey(registry.CURRENT_USER, steamKey+`\ActiveProcess`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetIntegerValue("ActiveUser"); err == nil {
			h.ActiveUser = uint32(v)
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, steamKey, registry.QUERY_VALUE); err == nil {
		h.AutoLogin, _, _ = k.GetStringValue("AutoLoginUser")
		k.Close()
	}
	return h
}

// running reports whether Steam says the game is running right now.
func running(appID int) bool {
	if k, err := registry.OpenKey(registry.CURRENT_USER, steamKey, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetIntegerValue("RunningAppID")
		k.Close()
		if err == nil && int(v) == appID {
			return true
		}
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, steamKey+`\Apps\`+strconv.Itoa(appID), registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Running")
	return err == nil && v != 0
}

var sigCache struct {
	sync.Mutex
	m map[string]bool
}

// signed reports whether the file carries a valid Authenticode signature.
// Steam's steam_api DLLs are signed by Valve; emulator DLLs aren't, and a
// patched one fails the hash check. No revocation or network lookups.
func signed(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	key := path + "|" + strconv.FormatInt(fi.Size(), 10) + "|" + fi.ModTime().Format(time.RFC3339Nano)
	sigCache.Lock()
	v, ok := sigCache.m[key]
	sigCache.Unlock()
	if ok {
		return v
	}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	data := &windows.WinTrustData{
		Size:             uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:         windows.WTD_UI_NONE,
		RevocationChecks: windows.WTD_REVOKE_NONE,
		UnionChoice:      windows.WTD_CHOICE_FILE,
		StateAction:      windows.WTD_STATEACTION_VERIFY,
		ProvFlags:        windows.WTD_CACHE_ONLY_URL_RETRIEVAL,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&windows.WinTrustFileInfo{
			Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
			FilePath: p16,
		}),
	}
	err = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	v = err == nil
	sigCache.Lock()
	if sigCache.m == nil {
		sigCache.m = map[string]bool{}
	}
	sigCache.m[key] = v
	sigCache.Unlock()
	return v
}
