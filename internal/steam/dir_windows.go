package steam

import gsteam "github.com/ApolloF/gamekit/steam"

// Dir returns Steam's install folder ("" if Steam isn't installed).
func Dir() string { return gsteam.Dir() }
