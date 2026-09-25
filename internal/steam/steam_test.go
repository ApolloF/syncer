package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseVDF(t *testing.T) {
	n := ParseVDF(strings.NewReader(`// comment
"users"
{
	"76561197960265729"
	{
		"AccountName"		"me"
		"MostRecent"		"1"
		"Path"		"C:\\Games\\x"
	}
}`))
	u := n.Get("Users", "76561197960265729")
	if u.Value("accountname") != "me" || u.Value("MostRecent") != "1" || u.Value("path") != `C:\Games\x` {
		t.Fatalf("bad parse: %+v", u)
	}
	if n.Get("users", "nope") != nil || n.Get("nope").Value("x") != "" {
		t.Fatal("missing keys should be nil/empty")
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// manifest writes an appmanifest for app id into the Steam folder d and
// creates its install folder.
func manifest(t *testing.T, d string, id, flags int) string {
	t.Helper()
	name := fmt.Sprintf("Game%d", id)
	write(t, filepath.Join(d, "steamapps", fmt.Sprintf("appmanifest_%d.acf", id)),
		fmt.Sprintf(`"AppState" { "appid" "%d" "name" "%s" "StateFlags" "%d" "installdir" "%s" }`, id, name, flags, name))
	dir := filepath.Join(d, "steamapps", "common", name)
	write(t, filepath.Join(dir, "game.exe"), "x")
	return dir
}

func TestAccounts(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "config", "loginusers.vdf"), `"users" {
		"76561197960265729" { "AccountName" "first" "Timestamp" "200" }
		"76561197960265730" { "AccountName" "second" "Timestamp" "100" }
		"76561197960265731" { "AccountName" "gone" "Timestamp" "900" }
	}`)
	for _, acc := range []string{"1", "2"} {
		if err := os.MkdirAll(filepath.Join(d, "userdata", acc), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name string
		h    Host
		want []string
	}{
		{"signed in now", Host{ActiveUser: 2}, []string{"2"}},
		{"auto login", Host{AutoLogin: "SECOND"}, []string{"2"}},
		{"active user without userdata", Host{ActiveUser: 3, AutoLogin: "first"}, []string{"1"}},
		{"last sign-in", Host{}, []string{"1"}},
	} {
		if got := accounts(d, tt.h); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: accounts = %v, want %v", tt.name, got, tt.want)
		}
	}
	// Older Steam: MostRecent wins over timestamps.
	write(t, filepath.Join(d, "config", "loginusers.vdf"), `"users" {
		"76561197960265729" { "MostRecent" "0" "Timestamp" "200" }
		"76561197960265730" { "MostRecent" "1" "Timestamp" "100" }
	}`)
	if got := accounts(d, Host{}); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("MostRecent: accounts = %v", got)
	}
}

func TestLibraryApps(t *testing.T) {
	d := t.TempDir()
	manifest(t, d, 10, 4)                                         // installed
	manifest(t, d, 20, 1026)                                      // waiting for an update: still installed
	manifest(t, d, 30, 4|2048)                                    // being uninstalled
	manifest(t, d, 40, 1)                                         // uninstalled
	write(t, filepath.Join(d, "steamapps", "appmanifest_50.acf"), // folder missing
		`"AppState" { "appid" "50" "StateFlags" "4" "installdir" "Missing" }`)
	write(t, filepath.Join(d, "steamapps", "appmanifest_60.acf"), // escapes common
		`"AppState" { "appid" "60" "StateFlags" "4" "installdir" "..\\.." }`)
	apps := Apps(d)
	for id, want := range map[int]bool{10: true, 20: true, 30: false, 40: false, 50: false, 60: false} {
		if a, ok := apps[id]; !ok || a.Installed != want {
			t.Errorf("app %d: %+v (found %v), want installed=%v", id, a, ok, want)
		}
	}
	if apps[60].Dir != "" || apps[50].Dir == "" {
		t.Errorf("install dirs: 50=%q 60=%q", apps[50].Dir, apps[60].Dir)
	}
}

func TestReadRemoteCache(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "rc.vdf"), `"413150" {
		"ChangeNumber" "65"
		"StardewValley/Saves/Farm_1/Farm_1" { "root" "4" "size" "12" "localtime" "1668432340" "time" "1668432338" }
		"remote.sav" { "root" "0" "size" "3" "time" "1668432000" }
		"/" { "root" "4" "localtime" "1" }
	}`)
	got := map[string]int64{}
	for _, e := range readRemoteCache(filepath.Join(d, "rc.vdf")) {
		got[e.rel] = e.localTime.Unix()
	}
	want := map[string]int64{`stardewvalley\saves\farm_1\farm_1`: 1668432340, "remote.sav": 1668432000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// Games can move a Steam Cloud root per OS (Cult of the Lamb's "game install"
// root lives in LocalLow on Windows), so files are matched by path suffix.
func TestInspectRootOverride(t *testing.T) {
	d := t.TempDir()
	saves := filepath.Join(d, "LocalLow", "Massive Monster", "Cult Of The Lamb", "saves")
	synced := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, n := range []string{"meta_0.json", "slot_0.json"} {
		write(t, filepath.Join(saves, n), "x")
		touch(t, filepath.Join(saves, n), synced)
	}
	es := []entry{{`saves\meta_0.json`, synced}, {`saves\slot_0.json`, synced}, {`saves\gone.json`, synced}}
	if st := inspect(saves, es); st.tracked != 2 || st.changed || st.modSave != "" {
		t.Errorf("override root: %+v", st)
	}
	if st := inspect(filepath.Join(d, "LocalLow", "Other"), es); st.tracked != 0 {
		t.Errorf("other folder: %+v", st)
	}
	// Same file name, different path: not the tracked file.
	write(t, filepath.Join(d, "x", "meta_0.json"), "x")
	if st := inspect(filepath.Join(d, "x"), es); st.tracked != 0 {
		t.Errorf("suffix must match the whole relative path: %+v", st)
	}
}

func TestTampered(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	d := t.TempDir()
	write(t, filepath.Join(d, "bin", "steam_api64.dll"), "x")
	if m := Tampered(d, yes); m != "" {
		t.Errorf("signed DLL flagged: %q", m)
	}
	if m := Tampered(d, no); !strings.HasPrefix(m, "steam_api64.dll") {
		t.Errorf("unsigned DLL: %q", m)
	}
	// DLC unlockers still run through Steam.
	write(t, filepath.Join(d, "bin", "cream_api.ini"), "x")
	if m := Tampered(d, no); m != "" {
		t.Errorf("unlocker flagged: %q", m)
	}
	write(t, filepath.Join(d, "bin", "steam_emu.ini"), "x")
	if m := Tampered(d, yes); m != "steam_emu.ini" {
		t.Errorf("emulator ini: %q", m)
	}
	g := t.TempDir()
	write(t, filepath.Join(g, "Game", "steam_settings", "steam_appid.txt"), "1")
	if m := Tampered(g, yes); m != "steam_settings" {
		t.Errorf("Goldberg settings: %q", m)
	}
	deep := t.TempDir()
	write(t, filepath.Join(deep, "Engine", "Binaries", "ThirdParty", "Steamworks", "Steamv157", "Win64", "steam_api64.dll"), "x")
	if m := Tampered(deep, no); !strings.HasPrefix(m, "steam_api64.dll") {
		t.Errorf("Unreal's Steamworks DLL not checked: %q", m)
	}
	write(t, filepath.Join(deep, "a", "b", "c", "d", "e", "f", "g", "h", "steam_emu.ini"), "x")
	if m := Tampered(deep, yes); m != "" {
		t.Errorf("too deep to search, got %q", m)
	}
	if Tampered("", no) != "" {
		t.Error("no folder should be clean")
	}
}

// fakeSteam builds a Steam folder with one signed-in account (2) that keeps
// app 222 in Steam Cloud: saves in <roaming>\Game\Saves\slot1\.
type fakeSteam struct {
	dir, roaming, saves, install string
	synced                       time.Time
}

func newFakeSteam(t *testing.T) fakeSteam {
	t.Helper()
	f := fakeSteam{dir: t.TempDir(), roaming: t.TempDir(), synced: time.Now().Add(-48 * time.Hour).Truncate(time.Second)}
	f.saves = filepath.Join(f.roaming, "Game", "Saves")
	write(t, filepath.Join(f.dir, "config", "loginusers.vdf"), `"users" {
		"76561197960265729" { "AccountName" "other" }
		"76561197960265730" { "AccountName" "me" }
	}`)
	ud := filepath.Join(f.dir, "userdata", "2")
	write(t, filepath.Join(ud, "config", "localconfig.vdf"), `"UserLocalConfigStore" {
		"Software" { "Valve" { "Steam" { "apps" { "444" { "cloudenabled" "0" } } } } }
	}`)
	write(t, filepath.Join(f.dir, "userdata", "1", "333", "remotecache.vdf"), `"333" {}`) // other account only
	ts := f.synced.Unix()
	write(t, filepath.Join(ud, "222", "remotecache.vdf"), fmt.Sprintf(`"222" {
		"Game/Saves/slot1/save.sav" { "root" "4" "size" "4" "localtime" "%d" }
		"Game/Saves/slot1/info.dat" { "root" "4" "size" "4" "localtime" "%d" }
	}`, ts, ts))
	for _, n := range []string{"save.sav", "info.dat"} {
		p := filepath.Join(f.saves, "slot1", n)
		write(t, p, "data")
		touch(t, p, f.synced)
	}
	write(t, filepath.Join(f.saves, "steam_autocloud.vdf"), `"steam_autocloud.vdf" { "accountid" "2" }`)
	f.install = manifest(t, f.dir, 222, 4)
	write(t, filepath.Join(f.install, "steam_api64.dll"), "x")
	manifest(t, f.dir, 333, 4)
	manifest(t, f.dir, 444, 4)
	manifest(t, f.dir, 555, 4) // installed, never synced
	return f
}

func (f fakeSteam) detect(h Host) *Cloud {
	h.ActiveUser = 2
	return DetectIn(f.dir, h)
}

func TestCheck(t *testing.T) {
	f := newFakeSteam(t)
	c := f.detect(Host{})
	for _, tt := range []struct {
		app  int
		dir  string
		want string
	}{
		{222, f.saves, ""},
		{222, filepath.Join(f.saves, "slot1"), ""},
		{222, filepath.Join(f.roaming, "Game", "Config"), ReasonUntracked},
		{333, f.saves, ReasonNoCloudData}, // cloud data belongs to the other account
		{444, f.saves, ReasonCloudOff},
		{555, f.saves, ReasonNoCloudData},
		{666, f.saves, ReasonNotInstalled}, // e.g. a repack: no appmanifest
		{0, f.saves, ReasonNoSteam},
	} {
		v := c.Check(tt.app, tt.dir)
		if v.Reason != tt.want || v.Covered != (tt.want == "") {
			t.Errorf("Check(%d, %s) = %+v, want reason %q", tt.app, tt.dir, v, tt.want)
		}
	}
	if v := DetectIn("", Host{}).Check(222, f.saves); v.Covered || v.Reason != ReasonNoSteam {
		t.Errorf("no Steam: %+v", v)
	}

	// A crack in the Steam install folder.
	if v := f.detect(Host{Signed: func(string) bool { return false }}).Check(222, f.saves); v.Covered ||
		!strings.HasPrefix(v.Reason, ReasonModified+":steam_api64.dll") {
		t.Errorf("unsigned steam_api: %+v", v)
	}
	write(t, filepath.Join(f.install, "steam_emu.ini"), "x")
	if v := f.detect(Host{}).Check(222, f.saves); v.Reason != ReasonModified+":steam_emu.ini" {
		t.Errorf("emulator: %+v", v)
	}
	os.Remove(filepath.Join(f.install, "steam_emu.ini"))

	// Steam Cloud switched off globally.
	write(t, filepath.Join(f.dir, "userdata", "2", "7", "remote", "sharedconfig.vdf"), `"UserRoamingConfigStore" { "CloudEnabled" "0" }`)
	if c := f.detect(Host{}); c.Check(222, f.saves).Reason != ReasonCloudOff || c.Enabled {
		t.Error("global cloud off should cover nothing")
	}
}

func TestCheckFreshness(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name    string
		setup   func(f fakeSteam)
		running bool
		want    string
	}{
		{"played outside Steam", func(f fakeSteam) {
			touch(t, filepath.Join(f.saves, "slot1", "save.sav"), old)
		}, false, ReasonOutside},
		{"still running through Steam", func(f fakeSteam) {
			touch(t, filepath.Join(f.saves, "slot1", "save.sav"), old)
		}, true, ""},
		{"just closed, Steam may still sync", func(f fakeSteam) {
			touch(t, filepath.Join(f.saves, "slot1", "save.sav"), time.Now().Add(-time.Minute))
		}, false, ""},
		{"new save slot Steam never saw", func(f fakeSteam) {
			p := filepath.Join(f.saves, "slot2", "save.sav")
			write(t, p, "new")
			touch(t, p, old)
		}, false, ReasonOutside},
		{"unrelated newer file", func(f fakeSteam) {
			p := filepath.Join(f.saves, "slot1", "log.txt")
			write(t, p, "log")
			touch(t, p, old)
		}, false, ""},
		{"backup copy deeper down", func(f fakeSteam) {
			p := filepath.Join(f.saves, "slot1", "backup", "old", "save.sav")
			write(t, p, "bak")
			touch(t, p, old)
		}, false, ""},
		{"script extender co-save", func(f fakeSteam) {
			write(t, filepath.Join(f.saves, "slot1", "save.skse"), "mod")
		}, false, ReasonModSaves + ":.skse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeSteam(t)
			tt.setup(f)
			v := f.detect(Host{Running: func(int) bool { return tt.running }}).Check(222, f.saves)
			if v.Reason != tt.want || v.Covered != (tt.want == "") {
				t.Errorf("got %+v, want reason %q", v, tt.want)
			}
		})
	}
}
