package steam

import gsteam "github.com/ApolloF/gamekit/steam"

// App is a game Steam installed: it has an appmanifest_<id>.acf in a library.
// Copies installed outside Steam (a crack, a repack) have none.
type App = gsteam.App

// Libraries returns Steam's own folder plus every library folder listed in
// its libraryfolders.vdf.
func Libraries(root string) []string { return gsteam.Libraries(root) }

// LibraryApps reads the app manifests of one Steam library.
func LibraryApps(lib string) []App { return gsteam.LibraryApps(lib) }

// Apps returns every game Steam knows as installed, across all libraries.
func Apps(root string) map[int]App { return gsteam.Apps(root) }
