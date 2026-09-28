package meta

import (
	"context"
	"time"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// ModSettings are the mod options every PC uses alike: two PCs that disagree
// on them (one sharing Vortex's mod list and the other not, one offering
// deployed mods the other can't take) work against each other. Each PC
// publishes its own in its device file; the newest change on any PC wins
// and is taken over by the others. PCs that never changed one since this
// came along publish theirs unstamped, and nothing is taken over until
// someone does (Settings shows when they differ).
type ModSettings struct {
	FindMods         bool      `json:"findMods"`
	AutoAddMods      bool      `json:"autoAddMods"`
	SyncDeployedMods bool      `json:"syncDeployedMods"`
	ShareVortexMods  bool      `json:"shareVortexMods"`
	ModsMaxGB        int       `json:"modsMaxGB"`
	Changed          time.Time `json:"changed,omitzero"`
}

// ModSettingsAdopted runs after this PC took over another PC's mod settings
// (set by the app: turning an option off may need more than the setting).
var ModSettingsAdopted func(ctx context.Context, c *syncthing.Client, old, now store.Settings)

// ModSettingsOf is what this PC publishes.
func ModSettingsOf(s store.Settings) ModSettings {
	return ModSettings{FindMods: s.FindMods, AutoAddMods: s.AutoAddMods, SyncDeployedMods: s.SyncDeployedMods,
		ShareVortexMods: s.ShareVortexMods, ModsMaxGB: s.ModsMaxGB, Changed: s.ModsChanged}
}

// Same reports whether two PCs use the same mod options (when they were
// changed aside).
func (m ModSettings) Same(o ModSettings) bool {
	m.Changed, o.Changed = time.Time{}, time.Time{}
	return m == o
}

// Apply writes the options into this PC's settings.
func (m ModSettings) Apply(s *store.Settings) {
	s.FindMods, s.AutoAddMods, s.SyncDeployedMods, s.ShareVortexMods = m.FindMods, m.AutoAddMods, m.SyncDeployedMods, m.ShareVortexMods
	if !s.FindMods { // the other options build on finding mods (as in Settings)
		s.AutoAddMods, s.SyncDeployedMods, s.ShareVortexMods = false, false, false
	}
	if m.ModsMaxGB > 0 || m.ModsMaxGB == -1 {
		s.ModsMaxGB = m.ModsMaxGB
	}
	s.ModsChanged = m.Changed
}

// NewModStamp is the time to stamp a change of the mod options made here:
// now, or just after the newest stamp any PC published, so a PC whose clock
// runs ahead can't keep a change made here from winning.
func NewModStamp() time.Time {
	t := time.Now().UTC()
	for _, df := range readDeviceFiles() {
		if df.Mods != nil && !df.Mods.Changed.Before(t) {
			t = df.Mods.Changed.Add(time.Millisecond)
		}
	}
	return t
}

// adoptModSettings takes over the newest mod options another PC published,
// when they are newer than this PC's.
func adoptModSettings(ctx context.Context, c *syncthing.Client, me string) {
	s := store.LoadSettings()
	best, from := ModSettingsOf(s), ""
	for _, df := range readOtherFiles(me) {
		if df.Mods != nil && df.Mods.Changed.After(best.Changed) {
			best, from = *df.Mods, df.Name
		}
	}
	if from == "" {
		return
	}
	now, err := store.UpdateSettings(best.Apply)
	if err != nil {
		logx.Printf("meta: take mod settings from %s: %v", from, err)
		return
	}
	logx.Printf("meta: took the mod settings changed on %s", from)
	if ModSettingsAdopted != nil {
		ModSettingsAdopted(ctx, c, s, now)
	}
}

// ModSettingsDiffer names the other PCs whose mod options differ from this
// PC's (only PCs whose Syncer shares them).
func ModSettingsDiffer(me string) []string {
	mine := ModSettingsOf(store.LoadSettings())
	var out []string
	for _, df := range readOtherFiles(me) {
		if df.Mods != nil && !df.Mods.Same(mine) {
			out = append(out, cmpName(df))
		}
	}
	return out
}

func cmpName(df DeviceFile) string {
	if df.Name != "" {
		return df.Name
	}
	return df.Device[:min(7, len(df.Device))]
}
