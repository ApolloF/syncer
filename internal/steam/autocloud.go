package steam

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// Steam writes steam_autocloud.vdf into every folder it keeps in Steam Cloud
// for a game using Auto-Cloud, naming the account whose cloud saves they are.
// It is the most direct sign that Steam Cloud manages a save folder.
const autocloudFile = "steam_autocloud.vdf"

// marker is what a save folder's steam_autocloud.vdf says.
type marker struct {
	found   bool
	account uint32 // accountid in it (0 = unreadable)
}

const (
	markerDepth   = 3    // per-account subfolders: Saves\7656…\steam_autocloud.vdf
	markerEntries = 2000 // stop looking in huge folders
	markerParents = 3    // Steam puts it in the Auto-Cloud root, which can be above the save folder
)

// findMarker looks for steam_autocloud.vdf in dir, in its subfolders a few
// levels down, and in its parents while stop allows (stop == nil: none).
// A marker naming an account on this PC (local) wins over any other.
func findMarker(dir string, local func(uint32) bool, stop func(string) bool) marker {
	var best marker
	consider := func(p string) bool {
		m := readMarker(p)
		if !m.found {
			return false
		}
		if !best.found || (!local(best.account) && local(m.account)) {
			best = m
		}
		return local(m.account)
	}
	if dir == "" {
		return best
	}
	root := filepath.Clean(dir)
	depth0 := strings.Count(root, string(filepath.Separator))
	n := 0
	done := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > markerEntries {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(filepath.Separator))-depth0 >= markerDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(d.Name(), autocloudFile) && consider(p) {
			done = true
			return filepath.SkipAll
		}
		return nil
	})
	if done || stop == nil {
		return best
	}
	p := root
	for i := 0; i < markerParents; i++ {
		parent := filepath.Dir(p)
		if parent == p || stop(parent) {
			break
		}
		p = parent
		if consider(filepath.Join(p, autocloudFile)) {
			break
		}
	}
	return best
}

// readMarker parses one steam_autocloud.vdf ({"accountid" "<id>"}).
func readMarker(p string) marker {
	if !isFile(p) {
		return marker{}
	}
	m := marker{found: true}
	n := readVDF(p)
	v := n.Get(autocloudFile).Value("accountid")
	if v == "" {
		for _, k := range n.Kids() { // whatever the root key is called
			if v = k.Value("accountid"); v != "" {
				break
			}
		}
	}
	if id, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32); err == nil {
		m.account = uint32(id)
	}
	return m
}
