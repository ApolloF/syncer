package steam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDetectIn(t *testing.T) {
	d := t.TempDir()
	// Two accounts; account 2 is the active one.
	write(t, filepath.Join(d, "config", "loginusers.vdf"), `"users" {
		"76561197960265729" { "MostRecent" "0" }
		"76561197960265730" { "MostRecent" "1" }
	}`)
	ud1 := filepath.Join(d, "userdata", "1")
	ud2 := filepath.Join(d, "userdata", "2")
	write(t, filepath.Join(ud1, "111", "remotecache.vdf"), `"111" {}`) // other account only
	write(t, filepath.Join(ud2, "222", "remotecache.vdf"), `"222" {}`) // Auto-Cloud
	write(t, filepath.Join(ud2, "333", "remote", "save.dat"), `x`)     // Cloud API
	write(t, filepath.Join(ud2, "444", "remotecache.vdf"), `"444" {}`) // switched off
	write(t, filepath.Join(ud2, "555", "remotecache.vdf"), ``)         // empty: never synced
	write(t, filepath.Join(ud2, "config", "localconfig.vdf"), `"UserLocalConfigStore" {
		"Software" { "Valve" { "Steam" { "apps" { "444" { "cloudenabled" "0" } } } } }
	}`)

	write(t, filepath.Join(ud2, "7", "remote", "sharedconfig.vdf"), `"UserRoamingConfigStore" { "CloudEnabled" "1" }`)
	c := DetectIn(d)
	for id, want := range map[int]bool{111: false, 222: true, 333: true, 444: false, 555: false, 666: false, 0: false} {
		if got := c.Covers(id); got != want {
			t.Errorf("Covers(%d) = %v, want %v", id, got, want)
		}
	}

	// Steam Cloud switched off globally: nothing is covered.
	write(t, filepath.Join(ud2, "7", "remote", "sharedconfig.vdf"), `"UserRoamingConfigStore" { "CloudEnabled" "0" }`)
	if c := DetectIn(d); c.Covers(222) || c.Enabled {
		t.Error("global cloud off should cover nothing")
	}

	if c := DetectIn(""); c.Found || c.Covers(222) {
		t.Error("no Steam should cover nothing")
	}
}
