package steam

import gsteam "github.com/ApolloF/gamekit/steam"

// Tampered returns the first sign that a Steam game's folder was cracked or
// runs on a Steam emulator: a marker file, Goldberg's steam_settings folder,
// or a steam_api DLL without a valid (Valve) signature. "" if none.
func Tampered(dir string, signed func(string) bool) string { return gsteam.Tampered(dir, signed) }
