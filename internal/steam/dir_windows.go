package steam

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// Dir returns Steam's install folder ("" if Steam isn't installed).
func Dir() string {
	for _, k := range []struct {
		root registry.Key
		path string
		name string
	}{
		{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := key.GetStringValue(k.name)
		key.Close()
		if d := clean(v); err == nil && d != "" && isDir(filepath.Join(d, "userdata")) {
			return d
		}
	}
	d := filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam")
	if isDir(filepath.Join(d, "userdata")) {
		return d
	}
	return ""
}
