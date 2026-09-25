// Package steam answers one question: is a game's save actually kept in Steam
// Cloud on this PC? The Ludusavi manifest only says a game *supports* Steam
// Cloud; that says nothing about a copy installed outside Steam, a game owned
// by a different account, or cloud sync switched off.
package steam

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// steamID64 of account id 0; userdata folders are named by the 32-bit account id.
const idBase = 76561197960265728

// Cloud describes which apps Steam Cloud really covers for the active account.
type Cloud struct {
	Found   bool         // Steam and a logged-in account were found
	Enabled bool         // Steam Cloud is on for the account
	apps    map[int]bool // apps with cloud data for the account
	off     map[int]bool // apps whose cloud sync the user switched off
}

// Covers reports whether appID's saves are synced by Steam Cloud here.
func (c Cloud) Covers(appID int) bool {
	return c.Found && c.Enabled && appID > 0 && c.apps[appID] && !c.off[appID]
}

// Detect inspects the Steam installation on this PC.
func Detect() Cloud { return DetectIn(Dir()) }

// DetectIn inspects the Steam installation at dir (for tests).
func DetectIn(dir string) Cloud {
	c := Cloud{apps: map[int]bool{}, off: map[int]bool{}}
	if dir == "" {
		return c
	}
	users := accounts(dir)
	if len(users) == 0 {
		return c
	}
	c.Found = true
	for _, u := range users {
		ud := filepath.Join(dir, "userdata", u)
		// Top-level store name differs per file (UserLocalConfigStore,
		// UserRoamingConfigStore), so look inside whatever the root holds.
		var stores []*Node
		for _, f := range []string{filepath.Join(ud, "config", "localconfig.vdf"), filepath.Join(ud, "7", "remote", "sharedconfig.vdf")} {
			for _, n := range readVDF(f).Kids() {
				stores = append(stores, n)
			}
		}
		enabled := true
		for _, st := range stores {
			// Global "Enable Steam Cloud" toggle.
			if st.Value("CloudEnabled") == "0" || st.Get("system").Value("EnableCloud") == "0" ||
				st.Get("Software", "Valve", "Steam").Value("CloudEnabled") == "0" {
				enabled = false
			}
		}
		if !enabled {
			continue
		}
		c.Enabled = true
		// Per-game "Keep game saves in the Steam Cloud" toggle.
		for _, st := range stores {
			for id, n := range st.Get("Software", "Valve", "Steam", "apps").Kids() {
				if n.Value("cloudenabled") == "0" {
					if i, err := strconv.Atoi(id); err == nil {
						c.off[i] = true
					}
				}
			}
		}
		es, _ := os.ReadDir(ud)
		for _, e := range es {
			id, err := strconv.Atoi(e.Name())
			if err != nil || !e.IsDir() || id < 10 {
				continue // 7 = Steam client itself
			}
			if hasCloudData(filepath.Join(ud, e.Name())) {
				c.apps[id] = true
			}
		}
	}
	return c
}

// hasCloudData: Steam writes remotecache.vdf for every app it cloud-syncs
// (including Auto-Cloud games that save outside userdata), and keeps files
// under remote\ for games using the Cloud API directly.
func hasCloudData(appDir string) bool {
	if fi, err := os.Stat(filepath.Join(appDir, "remotecache.vdf")); err == nil && fi.Size() > 0 {
		return true
	}
	es, err := os.ReadDir(filepath.Join(appDir, "remote"))
	return err == nil && len(es) > 0
}

// accounts returns userdata folder names for the account Steam logs into (the
// "MostRecent" one); all known accounts if that can't be told.
func accounts(dir string) []string {
	lu := readVDF(filepath.Join(dir, "config", "loginusers.vdf")).Get("users")
	var recent, all []string
	if lu != nil {
		for id64, u := range lu.Kids() {
			n, err := strconv.ParseUint(id64, 10, 64)
			if err != nil || n <= idBase {
				continue
			}
			acc := strconv.FormatUint(n-idBase, 10)
			if !isDir(filepath.Join(dir, "userdata", acc)) {
				continue
			}
			all = append(all, acc)
			if u.Value("MostRecent") == "1" {
				recent = append(recent, acc)
			}
		}
	}
	if len(recent) > 0 {
		return recent
	}
	if len(all) > 0 {
		return all
	}
	es, _ := os.ReadDir(filepath.Join(dir, "userdata"))
	for _, e := range es {
		if _, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() && e.Name() != "0" {
			all = append(all, e.Name())
		}
	}
	return all
}

func readVDF(p string) *Node {
	f, err := os.Open(p)
	if err != nil {
		return newNode()
	}
	defer f.Close()
	return ParseVDF(f)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func clean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(p))
}
