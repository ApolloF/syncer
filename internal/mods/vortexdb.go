package mods

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/ApolloF/syncer/internal/paths"
)

// Vortex keeps its state (which mods it knows, their metadata, which ones
// each profile enables) in a LevelDB database, %APPDATA%\Vortex\state.v2.
// Every leaf of its state is one key: the path to it joined by "###"
// ("persistent###mods###skyrimse###SkyUI###attributes###version"), with the
// value as JSON. Sharing Vortex's mod list between PCs (experimental) reads
// and writes the few keys below; everything else in the database is left
// alone. It is only opened while Vortex is closed: it holds the database
// exclusively, and so does Syncer while it looks.

const dbSep = "###"

// VortexDBPath is Vortex's state database.
func VortexDBPath() string { return filepath.Join(vortexRoot(), "state.v2") }

// ErrVortexOpen: Vortex has its database open.
var ErrVortexOpen = errors.New("Vortex is open")

// localLeaves are parts of a mod's record that only make sense on this PC:
// the id of the archive in this PC's downloads, and when this PC's Vortex
// last asked SMAPI about the mod (it changes every session, so sharing it
// would make every mod look changed all the time).
var localLeaves = map[string]bool{"archiveId": true, "attributes###lastSMAPIQuery": true}

// isLocalLeaf reports whether a part of a mod's record (or the part it is
// inside) stays on this PC.
func isLocalLeaf(leaf string) bool {
	for p := leaf; ; {
		if localLeaves[p] {
			return true
		}
		i := strings.LastIndex(p, dbSep)
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// Limits on what a mod record may hold (it may come from another PC).
const (
	maxModLeaves   = 2000
	maxLeafBytes   = 1 << 20
	maxGameMods    = 20000
	maxModIDLength = 255
)

// VortexMod is a mod as Vortex's database has it.
type VortexMod struct {
	// Leaves are the mod's record: the path below the mod ("attributes###
	// version") -> its JSON value. Parts only this PC can use are left out.
	Leaves  map[string]json.RawMessage `json:"leaves"`
	Enabled bool                       `json:"enabled"`
}

// Folder is the mod's folder in the staging folder.
func (m VortexMod) Folder(id string) string {
	var p string
	if raw, ok := m.Leaves["installationPath"]; ok && json.Unmarshal(raw, &p) == nil && p != "" {
		return p
	}
	return id
}

// Installed reports whether Vortex has finished installing the mod.
func (m VortexMod) Installed() bool {
	var st string
	raw, ok := m.Leaves["state"]
	return ok && json.Unmarshal(raw, &st) == nil && st == "installed"
}

// Hash identifies the mod's record and whether it's enabled.
func (m VortexMod) Hash() string {
	keys := make([]string, 0, len(m.Leaves))
	for k := range m.Leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		var buf bytes.Buffer
		if json.Compact(&buf, m.Leaves[k]) != nil {
			buf.Reset()
			buf.Write(m.Leaves[k])
		}
		fmt.Fprintf(h, "%d:%s=%d:%s;", len(k), k, buf.Len(), buf.Bytes())
	}
	fmt.Fprintf(h, "enabled=%t", m.Enabled)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Check reports whether a mod record (from another PC) can be written to
// this PC's database: a plain id and folder name, sane leaves.
func (m VortexMod) Check(id string) error {
	if !ValidModID(id) {
		return fmt.Errorf("bad mod id %q", id)
	}
	if !ValidModID(m.Folder(id)) {
		return fmt.Errorf("mod %s: bad folder name", id)
	}
	if len(m.Leaves) == 0 || len(m.Leaves) > maxModLeaves {
		return fmt.Errorf("mod %s: %d parts", id, len(m.Leaves))
	}
	for k, v := range m.Leaves {
		if !validLeafPath(k) || len(v) > maxLeafBytes || !json.Valid(v) {
			return fmt.Errorf("mod %s: bad part %q", id, k)
		}
	}
	return nil
}

// ValidModID reports whether id can be a mod id and a folder name in the
// staging folder: one plain name, nothing Windows treats specially.
func ValidModID(id string) bool {
	if id == "" || len(id) > maxModIDLength || !utf8.ValidString(id) || strings.Contains(id, dbSep) {
		return false
	}
	if strings.ContainsAny(id, `/\:*?"<>|`) || strings.ContainsFunc(id, func(r rune) bool { return r < 32 }) {
		return false
	}
	if id == "." || id == ".." || strings.HasSuffix(id, ".") || strings.HasSuffix(id, " ") || strings.HasPrefix(id, " ") {
		return false
	}
	return !reservedName(id) && !strings.HasPrefix(strings.ToLower(id), "__vortex")
}

func validLeafPath(k string) bool {
	if k == "" || len(k) > 1024 || !utf8.ValidString(k) {
		return false
	}
	for _, seg := range strings.Split(k, dbSep) {
		if seg == "" {
			return false
		}
	}
	return !isLocalLeaf(k)
}

// GameMods is one game's mods in Vortex on this PC.
type GameMods struct {
	// Profile is the game's active profile, where mods are enabled ("" if
	// Vortex has none for the game yet).
	Profile string
	Mods    map[string]VortexMod
}

// VortexDB is Vortex's database, open while Vortex is closed.
type VortexDB struct {
	db *leveldb.DB
}

// OpenVortexDB opens Vortex's database; ErrVortexOpen if Vortex holds it.
func OpenVortexDB() (*VortexDB, error) {
	p := VortexDBPath()
	if _, err := os.Stat(filepath.Join(p, "CURRENT")); err != nil {
		return nil, fmt.Errorf("Vortex's database isn't on this PC: %w", err)
	}
	db, err := leveldb.OpenFile(p, &opt.Options{ErrorIfMissing: true, NoSync: false})
	if err != nil {
		if isLockErr(err) {
			return nil, ErrVortexOpen
		}
		return nil, fmt.Errorf("can't open Vortex's database: %w", err)
	}
	return &VortexDB{db: db}, nil
}

func isLockErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "being used by another process") || strings.Contains(s, "access is denied") ||
		strings.Contains(s, "sharing violation") || strings.Contains(s, "locked")
}

// Close closes the database.
func (v *VortexDB) Close() error { return v.db.Close() }

func key(parts ...string) []byte { return []byte(strings.Join(parts, dbSep)) }

func (v *VortexDB) getString(k []byte) string {
	b, err := v.db.Get(k, nil)
	if err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s
}

// Mods reads a game's mods and which ones its active profile enables.
func (v *VortexDB) Mods(game string) (GameMods, error) {
	if !validGameID(game) {
		return GameMods{}, ErrUnsafe
	}
	gm := GameMods{Mods: map[string]VortexMod{}}
	prefix := key("persistent", "mods", game)
	prefix = append(prefix, dbSep...)
	it := v.db.NewIterator(util.BytesPrefix(prefix), nil)
	for it.Next() {
		rest := string(it.Key()[len(prefix):])
		id, leaf, ok := strings.Cut(rest, dbSep)
		if !ok || !ValidModID(id) || isLocalLeaf(leaf) {
			continue
		}
		m, ok := gm.Mods[id]
		if !ok {
			if len(gm.Mods) >= maxGameMods {
				continue
			}
			m = VortexMod{Leaves: map[string]json.RawMessage{}}
		}
		m.Leaves[leaf] = append(json.RawMessage(nil), it.Value()...)
		gm.Mods[id] = m
	}
	it.Release()
	if err := it.Error(); err != nil {
		return GameMods{}, err
	}
	gm.Profile = v.activeProfile(game)
	if gm.Profile != "" {
		for id, m := range gm.Mods {
			m.Enabled = v.enabled(gm.Profile, id)
			gm.Mods[id] = m
		}
	}
	return gm, nil
}

// activeProfile is the profile Vortex last used for game, if it is one of
// that game's.
func (v *VortexDB) activeProfile(game string) string {
	p := v.getString(key("settings", "profiles", "lastActiveProfile", game))
	if p == "" || strings.Contains(p, dbSep) || v.getString(key("persistent", "profiles", p, "gameId")) != game {
		return ""
	}
	return p
}

func (v *VortexDB) enabled(profile, id string) bool {
	b, err := v.db.Get(key("persistent", "profiles", profile, "modState", id, "enabled"), nil)
	return err == nil && string(bytes.TrimSpace(b)) == "true"
}

// profilesOf lists the profiles of a game.
func (v *VortexDB) profilesOf(game string) []string {
	prefix := append(key("persistent", "profiles"), dbSep...)
	it := v.db.NewIterator(util.BytesPrefix(prefix), nil)
	defer it.Release()
	var out []string
	for it.Next() {
		rest := string(it.Key()[len(prefix):])
		pid, leaf, ok := strings.Cut(rest, dbSep)
		if ok && leaf == "gameId" {
			var g string
			if json.Unmarshal(it.Value(), &g) == nil && g == game {
				out = append(out, pid)
			}
		}
	}
	return out
}

// VortexChange is one change to a game's mods.
type VortexChange struct {
	ID string
	// Mod is the new record; nil removes the mod.
	Mod *VortexMod
}

// Apply writes changes to a game's mods in one batch: a changed mod's
// record is replaced (keeping the parts only this PC uses) and it is
// enabled or disabled in the game's active profile; a removed mod goes
// from Vortex's list and from every profile of the game.
func (v *VortexDB) Apply(game, profile string, changes []VortexChange) error {
	if !validGameID(game) {
		return ErrUnsafe
	}
	b := new(leveldb.Batch)
	var profiles []string
	now := json.RawMessage(fmt.Sprint(time.Now().UnixMilli()))
	for _, c := range changes {
		if c.Mod != nil {
			if err := c.Mod.Check(c.ID); err != nil {
				return err
			}
		} else if !ValidModID(c.ID) {
			return fmt.Errorf("bad mod id %q", c.ID)
		}
		modPrefix := append(key("persistent", "mods", game, c.ID), dbSep...)
		it := v.db.NewIterator(util.BytesPrefix(modPrefix), nil)
		for it.Next() {
			leaf := string(it.Key()[len(modPrefix):])
			if c.Mod != nil && isLocalLeaf(leaf) {
				continue
			}
			b.Delete(append([]byte(nil), it.Key()...))
		}
		it.Release()
		if err := it.Error(); err != nil {
			return err
		}
		if c.Mod == nil {
			if profiles == nil {
				profiles = v.profilesOf(game)
			}
			for _, p := range profiles {
				stPrefix := append(key("persistent", "profiles", p, "modState", c.ID), dbSep...)
				it := v.db.NewIterator(util.BytesPrefix(stPrefix), nil)
				for it.Next() {
					b.Delete(append([]byte(nil), it.Key()...))
				}
				it.Release()
			}
			continue
		}
		for leaf, val := range c.Mod.Leaves {
			b.Put(append(append([]byte(nil), modPrefix...), leaf...), val)
		}
		if profile != "" && v.enabled(profile, c.ID) != c.Mod.Enabled {
			b.Put(key("persistent", "profiles", profile, "modState", c.ID, "enabled"), []byte(fmt.Sprint(c.Mod.Enabled)))
			b.Put(key("persistent", "profiles", profile, "modState", c.ID, "enabledTime"), now)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	// Vortex then asks to deploy the game, as after a change made in it.
	b.Put(key("persistent", "deployment", "needToDeploy", game), []byte("true"))
	return v.db.Write(b, &opt.WriteOptions{Sync: true})
}

// VortexDBStamp changes whenever Vortex's database changes on disk.
func VortexDBStamp() string {
	es, err := os.ReadDir(VortexDBPath())
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, e := range es {
		if fi, err := e.Info(); err == nil && !e.IsDir() && e.Name() != "LOCK" {
			fmt.Fprintf(&sb, "%s|%d|%d;", e.Name(), fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return sb.String()
}

// VortexStateBackupRoot is where copies of Vortex's database are kept
// before Syncer writes to it.
var VortexStateBackupRoot = func() string {
	return filepath.Join(paths.Root(paths.Local), "Syncer", "vortex-state")
}

// keepStateBackups is how many copies of Vortex's database are kept.
const keepStateBackups = 5

// BackupVortexDB copies Vortex's database (while it's closed) so it can be
// put back by hand, and drops the oldest copies.
func BackupVortexDB() (string, error) {
	src := VortexDBPath()
	root := VortexStateBackupRoot()
	dst := filepath.Join(root, time.Now().Format("20060102-150405.000"))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	es, err := os.ReadDir(src)
	if err != nil {
		return "", err
	}
	for _, e := range es {
		if e.IsDir() || e.Name() == "LOCK" {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), fi.ModTime()); err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
	}
	if old, err := os.ReadDir(root); err == nil {
		var names []string
		for _, e := range old {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for len(names) > keepStateBackups {
			_ = os.RemoveAll(filepath.Join(root, names[0]))
			names = names[1:]
		}
	}
	return dst, nil
}

// ---- sharing the mod list between PCs --------------------------------------

// ShareEntry is one mod in the list a PC shares: its record as of At, or
// Gone once it was removed.
type ShareEntry struct {
	At   int64      `json:"at"` // unix nanoseconds; 1 = as found when sharing started
	Hash string     `json:"hash,omitempty"`
	Gone bool       `json:"gone,omitempty"`
	Mod  *VortexMod `json:"mod,omitempty"`
}

// ShareList is the mod lists a PC shares, by Vortex game id and mod id.
type ShareList map[string]map[string]ShareEntry

// Valid drops what can't be trusted in a list from another PC.
func (l ShareList) Valid() ShareList {
	out := ShareList{}
	for g, ms := range l {
		if !validGameID(g) || len(ms) > maxGameMods {
			continue
		}
		clean := map[string]ShareEntry{}
		for id, e := range ms {
			if e.At <= 0 || e.At > time.Now().Add(time.Hour).UnixNano() { // one from the future would win forever
				continue
			}
			if e.Gone {
				if ValidModID(id) {
					clean[id] = ShareEntry{At: e.At, Gone: true}
				}
				continue
			}
			if e.Mod == nil || e.Mod.Check(id) != nil {
				continue
			}
			e.Hash = e.Mod.Hash()
			clean[id] = e
		}
		out[g] = clean
	}
	return out
}
