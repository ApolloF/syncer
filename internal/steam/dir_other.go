//go:build !windows

package steam

// Dir returns Steam's install folder; Syncer only supports Steam on Windows.
func Dir() string { return "" }
