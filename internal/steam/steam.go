// Package steam answers one question: is a game's save actually kept in Steam
// Cloud on this PC? The Ludusavi manifest only says a game *supports* Steam
// Cloud; that says nothing about a copy installed outside Steam, a cracked or
// modded copy, a game owned by a different account, or cloud sync switched off.
package steam

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// steamID64 of account id 0; userdata folders are named by the 32-bit account id.
const idBase = 76561197960265728

// Reasons a save isn't kept in Steam Cloud, from Check. Some carry a detail
// after a colon ("modified:steam_emu.ini", "mod-saves:.skse").
const (
	ReasonNoSteam      = "no-steam"      // Steam or a signed-in account wasn't found
	ReasonCloudOff     = "cloud-off"     // Steam Cloud is off, for the account or this game
	ReasonNotInstalled = "not-installed" // the game isn't installed through Steam
	ReasonModified     = "modified"      // crack or Steam emulator files in the game folder
	ReasonNoCloudData  = "no-cloud-data" // the account has no cloud saves for the game
	ReasonUntracked    = "untracked"     // Steam Cloud syncs other files of the game, not this folder
	ReasonModSaves     = "mod-saves"     // mod co-saves Steam Cloud skips (SKSE, Seamless Co-op, …)
	ReasonOutside      = "outside-steam" // saves changed after Steam last synced them
)

// Verdict is the outcome of Check.
type Verdict struct {
	Covered bool
	Reason  string // why not; "" when covered
}

// Host is what Steam's files don't say: which account is signed in right
// now and what's running. Detect fills it in from the registry and Windows;
// tests pass their own.
type Host struct {
	ActiveUser uint32                 // account id Steam is signed in with (0 = Steam closed / unknown)
	AutoLogin  string                 // account name Steam signs in with automatically
	Running    func(appID int) bool   // Steam reports the game running
	Signed     func(path string) bool // the file carries a valid Authenticode signature
}

// Cloud describes which apps Steam Cloud really covers for the active account.
type Cloud struct {
	Found   bool // Steam and a logged-in account were found
	Enabled bool // Steam Cloud is on for the account

	dir      string
	host     Host
	accounts []string        // userdata folders of the account(s) checked
	apps     map[int]string  // apps with cloud data -> account whose userdata holds it
	off      map[int]bool    // apps whose cloud sync the user switched off
	libs     map[int]App     // games Steam installed
	mu       sync.Mutex      // guards the lazily filled caches below
	tracked  map[int][]entry // remotecache.vdf per app
	tampered map[int]string  // crack/emulator marker per app ("" = clean)
}

// Detect inspects the Steam installation on this PC.
func Detect() *Cloud { return DetectIn(Dir(), hostInfo()) }

// DetectIn inspects the Steam installation at dir (for tests).
func DetectIn(dir string, h Host) *Cloud {
	if h.Running == nil {
		h.Running = func(int) bool { return false }
	}
	if h.Signed == nil {
		h.Signed = func(string) bool { return true }
	}
	c := &Cloud{dir: dir, host: h, apps: map[int]string{}, off: map[int]bool{}, libs: map[int]App{},
		tracked: map[int][]entry{}, tampered: map[int]string{}}
	if dir == "" {
		return c
	}
	c.accounts = accounts(dir, h)
	if len(c.accounts) == 0 {
		return c
	}
	c.Found = true
	c.libs = Apps(dir)
	for _, u := range c.accounts {
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
			if _, seen := c.apps[id]; !seen && hasCloudData(filepath.Join(ud, e.Name())) {
				c.apps[id] = u
			}
		}
	}
	return c
}

// Check reports whether Steam Cloud really keeps the saves in saveDir for
// appID on this PC. Everything must hold: Steam installed the game, its files
// weren't replaced by a crack or Steam emulator, the account has cloud saves
// for it, Steam Cloud tracks files in this very folder, there are no mod
// saves it skips, and Steam has seen the latest save.
func (c *Cloud) Check(appID int, saveDir string) Verdict {
	no := func(r string) Verdict { return Verdict{Reason: r} }
	switch {
	case c == nil || !c.Found || appID <= 0:
		return no(ReasonNoSteam)
	case !c.Enabled || c.off[appID]:
		return no(ReasonCloudOff)
	}
	app, ok := c.libs[appID]
	if !ok || !app.Installed {
		return no(ReasonNotInstalled)
	}
	if m := c.tamperedApp(app); m != "" {
		return no(ReasonModified + ":" + m)
	}
	if _, ok := c.apps[appID]; !ok {
		return no(ReasonNoCloudData)
	}
	st := inspect(saveDir, c.entries(appID))
	if st.tracked == 0 {
		return no(ReasonUntracked)
	}
	if st.modSave != "" {
		return no(ReasonModSaves + ":" + st.modSave)
	}
	if st.changed && !c.host.Running(appID) {
		return no(ReasonOutside)
	}
	return Verdict{Covered: true}
}

// entries returns the files Steam Cloud tracks for appID.
func (c *Cloud) entries(appID int) []entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if es, ok := c.tracked[appID]; ok {
		return es
	}
	es := readRemoteCache(filepath.Join(c.dir, "userdata", c.apps[appID], strconv.Itoa(appID), "remotecache.vdf"))
	c.tracked[appID] = es
	return es
}

func (c *Cloud) tamperedApp(app App) string {
	c.mu.Lock()
	m, ok := c.tampered[app.ID]
	c.mu.Unlock()
	if ok {
		return m
	}
	m = Tampered(app.Dir, c.host.Signed)
	c.mu.Lock()
	c.tampered[app.ID] = m
	c.mu.Unlock()
	return m
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

// accounts returns the userdata folder of the account Steam uses on this PC.
// Current Steam builds no longer mark it "MostRecent" in loginusers.vdf, so
// the registry is asked first. All known accounts if it can't be told.
func accounts(dir string, h Host) []string {
	type user struct {
		acc, name string
		recent    bool
		ts        int64
	}
	var users []user
	for id64, u := range readVDF(filepath.Join(dir, "config", "loginusers.vdf")).Get("users").Kids() {
		n, err := strconv.ParseUint(id64, 10, 64)
		if err != nil || n <= idBase {
			continue
		}
		acc := strconv.FormatUint(n-idBase, 10)
		if !isDir(filepath.Join(dir, "userdata", acc)) {
			continue
		}
		ts, _ := strconv.ParseInt(u.Value("Timestamp"), 10, 64)
		users = append(users, user{acc, u.Value("AccountName"), u.Value("MostRecent") == "1", ts})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].acc < users[j].acc })

	// Signed in right now.
	if h.ActiveUser != 0 {
		if acc := strconv.FormatUint(uint64(h.ActiveUser), 10); isDir(filepath.Join(dir, "userdata", acc)) {
			return []string{acc}
		}
	}
	// Signs in automatically when Steam starts.
	if h.AutoLogin != "" {
		for _, u := range users {
			if strings.EqualFold(u.name, h.AutoLogin) {
				return []string{u.acc}
			}
		}
	}
	// Older Steam builds mark the last account.
	var all []string
	for _, u := range users {
		if u.recent {
			return []string{u.acc}
		}
		all = append(all, u.acc)
	}
	// The account that signed in last.
	best := -1
	for i, u := range users {
		if u.ts > 0 && (best < 0 || u.ts > users[best].ts) {
			best = i
		}
	}
	if best >= 0 {
		return []string{users[best].acc}
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

// within reports whether child is parent or lies below it (case-insensitive).
func within(parent, child string) bool {
	parent, child = strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(child))
	if parent == child {
		return true
	}
	if !strings.HasSuffix(parent, `\`) && !strings.HasSuffix(parent, "/") {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(child, parent)
}
