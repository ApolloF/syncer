// Package accounts lets several people keep their own saves of the same game
// on the same PCs. A game is shared (one Syncthing folder, as always) until it
// is split: then every account gets its own folder, synced between all PCs.
// On each PC the active account's folder sits at the game's real save path
// and the other accounts' folders wait in a vault next to it; switching
// account swaps them (see ops.go).
//
// Everything here is shared through the syncer-meta folder: every PC
// publishes its own copy of the accounts and of the split/merge records, and
// every PC combines what the others published (newest wins), so nothing is
// ever written by two PCs at once.
package accounts

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/conflict"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

// Feature is what a PC advertises in its metadata when it understands split
// games. Splitting waits until every paired PC does: an older Syncer would
// keep syncing the game's shared folder and merge the accounts' saves again.
const Feature = "accounts-v1"

// Limits on what is accepted from other PCs (and created here).
const (
	MaxAccounts  = 16
	maxRecords   = 2000
	maxActiveLog = 200
	maxName      = 40
)

// Account is one person.
type Account struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Color   string    `json:"color,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Deleted bool      `json:"deleted,omitempty"`
}

// ActiveEntry says from when an account was the active one on a PC.
type ActiveEntry struct {
	Account string    `json:"account"`
	Since   time.Time `json:"since"`
}

// Record kinds.
const (
	KindSplit = "split"
	KindMerge = "merge"
)

// Record is the latest decision about one game: split into per-account
// folders, or merged back into one shared folder. The highest Gen wins.
type Record struct {
	Kind  string `json:"kind"`
	Game  string `json:"game"` // the shared folder id
	Label string `json:"label"`
	Root  string `json:"root"` // the save folder, portably
	Rel   string `json:"rel"`
	// Accounts that have their own folder (a split), or had one (a merge).
	Accounts []string `json:"accounts"`
	// Assign gives conflict copies (by path relative to the save folder,
	// forward slashes) to an account: that account's saves get the copy's
	// content under the real name. Every PC splits with the same map, so all
	// PCs build identical trees.
	Assign map[string]string `json:"assign,omitempty"`
	// Fresh are accounts added after the game was split: they start with no
	// saves (a PC splitting only now gives them an empty folder too).
	Fresh []string `json:"fresh,omitempty"`
	// Files is how many save files each account started with (a split), so
	// a PC can tell when an account's saves haven't reached it yet.
	Files int `json:"files,omitempty"`
	// Winner is whose saves became the shared ones (a merge).
	Winner  string    `json:"winner,omitempty"`
	Gen     int       `json:"gen"`
	Created time.Time `json:"created"`
	By      string    `json:"by"` // device id that made the decision
	// Origin is the PC that split the shared folder (kept when accounts
	// are added later): only its copy of the saves seeds the accounts.
	Origin string `json:"origin,omitempty"`
	// ParentGen: the Gen of the split an account was added to (0 for a
	// decision made by a person). Such an addition never beats a merge
	// made meanwhile.
	ParentGen int `json:"parentGen,omitempty"`
}

// Seeder is the PC whose saves became every account's at the split.
func (r Record) Seeder() string {
	if r.Origin != "" {
		return r.Origin
	}
	return r.By
}

// Shared is what every PC publishes about accounts.
type Shared struct {
	Accounts  []Account     `json:"accounts,omitempty"`
	Active    string        `json:"active,omitempty"`
	ActiveLog []ActiveEntry `json:"activeLog,omitempty"`
	Records   []Record      `json:"records,omitempty"`
}

// State is this PC's accounts file.
type State struct {
	Shared
	// Applied: the record (see Stamp) of each game this PC has carried out.
	Applied map[string]string `json:"applied,omitempty"`
	// Errors: why carrying out a game's record didn't work yet (retried).
	Errors map[string]string `json:"errors,omitempty"`
}

// Stamp identifies a record: two PCs may make different records with the
// same Gen at the same time, and only one of them wins.
func (r Record) Stamp() string {
	return fmt.Sprintf("%d|%s|%s", r.Gen, r.Created.UTC().Format(time.RFC3339Nano), r.By)
}

// IsFresh reports whether account starts this game with no saves.
func (r Record) IsFresh(account string) bool {
	for _, a := range r.Fresh {
		if a == account {
			return true
		}
	}
	return false
}

var (
	idPattern    = regexp.MustCompile(`^[a-z2-7]{6}$`)
	colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// ValidAccountID reports whether id is a well-formed account id.
func ValidAccountID(id string) bool { return idPattern.MatchString(id) }

const sep = ".u-"

// FolderID is the Syncthing folder id of an account's saves of a game.
func FolderID(game, account string) string { return game + sep + account }

// ParseFolderID splits an account folder id. ok is false for other folders.
func ParseFolderID(id string) (game, account string, ok bool) {
	i := strings.LastIndex(id, sep)
	if i <= 0 {
		return "", "", false
	}
	game, account = id[:i], id[i+len(sep):]
	if !ValidAccountID(account) || !paths.ValidID(game) || strings.Contains(game, sep) {
		return "", "", false
	}
	return game, account, true
}

// ---- local file ----------------------------------------------------------------

// dir is where the accounts file and the journal live (a var for tests).
var dir = paths.AppDir

func stateFile() string { return filepath.Join(dir(), "accounts.json") }

var mu sync.Mutex

// Load reads this PC's accounts file.
func Load() State {
	var s State
	if b, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Applied == nil {
		s.Applied = map[string]string{}
	}
	if s.Errors == nil {
		s.Errors = map[string]string{}
	}
	return s
}

// Update changes the accounts file under a lock (the window and the
// background task are separate processes).
func Update(fn func(*State) error) (State, error) {
	mu.Lock()
	defer mu.Unlock()
	defer store.FileLock("accounts")()
	s := Load()
	if err := fn(&s); err != nil {
		return s, err
	}
	return s, store.WriteJSON(stateFile(), s)
}

// ---- queries -------------------------------------------------------------------

// Live returns the accounts that aren't deleted, oldest first.
func (s Shared) Live() []Account {
	var out []Account
	for _, a := range s.Accounts {
		if !a.Deleted {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get finds a live account.
func (s Shared) Get(id string) (Account, bool) {
	for _, a := range s.Accounts {
		if a.ID == id && !a.Deleted {
			return a, true
		}
	}
	return Account{}, false
}

// Name is an account's name, or "" for an unknown one.
func (s Shared) Name(id string) string {
	a, _ := s.Get(id)
	return a.Name
}

// ActiveID is the account this PC plays as: the one chosen here, or the
// oldest account until one was chosen. "" when there are no accounts.
func (s Shared) ActiveID() string {
	if _, ok := s.Get(s.Active); ok {
		return s.Active
	}
	if l := s.Live(); len(l) > 0 {
		return l[0].ID
	}
	return ""
}

// Record returns the latest record of a game.
func (s Shared) Record(game string) (Record, bool) {
	for _, r := range s.Records {
		if r.Game == game {
			return r, true
		}
	}
	return Record{}, false
}

// Split returns a game's split record when the game is split right now.
func (s Shared) Split(game string) (Record, bool) {
	r, ok := s.Record(game)
	return r, ok && r.Kind == KindSplit
}

// Splits lists the games that are split right now.
func (s Shared) Splits() []Record {
	var out []Record
	for _, r := range s.Records {
		if r.Kind == KindSplit {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// Has reports whether account has its own folder in a split record.
func (r Record) Has(account string) bool {
	for _, a := range r.Accounts {
		if a == account {
			return true
		}
	}
	return false
}

// LivePath resolves the game's save folder on this PC. Records come from
// other PCs, so the path is checked like any shared folder's.
func (r Record) LivePath() (string, bool) {
	p, err := resolveLive(r)
	return p, err == nil
}

// errNoLive is resolveLive's error for a path that isn't a save folder at all.
var errNoLive = errors.New("the game's save folder isn't a valid place on this PC")

// resolveLive is LivePath with the reason it's refused: paths.ErrLink when
// this PC reaches the folder through a link. A var so tests can put save
// folders in a temp dir.
var resolveLive = func(r Record) (string, error) {
	p, ok := paths.Resolve(r.Root, r.Rel)
	if !ok {
		return "", errNoLive
	}
	if err := paths.CheckSyncable(p); err != nil {
		return "", err
	}
	return p, nil
}

// ---- combining what the PCs published -----------------------------------------

// Combine merges other PCs' published accounts and records into s: per
// account the newest change wins (deleting is a change), per game the record
// with the highest Gen. Invalid entries from peers are dropped. It reports
// whether anything changed.
func (s *State) Combine(peers []Shared) bool {
	changed := false
	acc := map[string]int{}
	for i, a := range s.Accounts {
		acc[a.ID] = i
	}
	rec := map[string]int{}
	for i, r := range s.Records {
		rec[r.Game] = i
	}
	for _, p := range peers {
		for _, a := range p.Accounts {
			if !validAccount(a) {
				continue
			}
			if i, ok := acc[a.ID]; ok {
				if newer(a, s.Accounts[i]) {
					s.Accounts[i] = a
					changed = true
				}
				continue
			}
			if len(s.Accounts) >= MaxAccounts*4 { // tombstones count too
				continue
			}
			acc[a.ID] = len(s.Accounts)
			s.Accounts = append(s.Accounts, a)
			changed = true
		}
		for _, r := range p.Records {
			if !ValidRecord(r) {
				continue
			}
			if i, ok := rec[r.Game]; ok {
				if Later(r, s.Records[i]) {
					s.Records[i] = r
					changed = true
				}
				continue
			}
			if len(s.Records) >= maxRecords {
				continue
			}
			rec[r.Game] = len(s.Records)
			s.Records = append(s.Records, r)
			changed = true
		}
	}
	return changed
}

func newer(a, b Account) bool {
	if !a.Updated.Equal(b.Updated) {
		return a.Updated.After(b.Updated)
	}
	if a.Deleted != b.Deleted {
		return a.Deleted // a tie between delete and edit: deleted stays deleted
	}
	return a.Name+a.Color > b.Name+b.Color // any stable pick, the same on every PC
}

// Later reports whether record a supersedes b (the same game).
func Later(a, b Record) bool {
	// An account added to a split doesn't undo a merge decided meanwhile
	// (both were built on the same split); the other way round a merge does.
	if a.ParentGen > 0 && b.Kind == KindMerge && b.Gen > a.ParentGen {
		return false
	}
	if b.ParentGen > 0 && a.Kind == KindMerge && a.Gen > b.ParentGen {
		return true
	}
	if a.Gen != b.Gen {
		return a.Gen > b.Gen
	}
	if !a.Created.Equal(b.Created) {
		return a.Created.After(b.Created)
	}
	return a.By > b.By
}

func validAccount(a Account) bool {
	return ValidAccountID(a.ID) && len(a.Name) <= maxName && (a.Deleted || strings.TrimSpace(a.Name) != "") &&
		(a.Color == "" || colorPattern.MatchString(a.Color)) && !a.Updated.IsZero()
}

// ValidRecord checks a record received from another PC.
func ValidRecord(r Record) bool {
	if (r.Kind != KindSplit && r.Kind != KindMerge) || r.Gen <= 0 || r.ParentGen < 0 || r.ParentGen >= r.Gen ||
		!paths.ValidID(r.Game) ||
		strings.EqualFold(r.Game, "syncer-meta") || r.Files < 0 ||
		strings.Contains(r.Game, sep) || len(r.Accounts) == 0 || len(r.Accounts) > MaxAccounts || len(r.Label) > 200 {
		return false
	}
	// A save folder this PC reaches through a link is still a valid record:
	// running it tells the user why it can't be split here.
	if _, err := resolveLive(r); err != nil && !errors.Is(err, paths.ErrLink) {
		return false
	}
	seen := map[string]bool{}
	for _, a := range r.Accounts {
		if !ValidAccountID(a) || seen[a] || !paths.ValidID(FolderID(r.Game, a)) {
			return false
		}
		seen[a] = true
	}
	if r.Kind == KindMerge && !seen[r.Winner] {
		return false
	}
	for _, a := range r.Fresh {
		if !seen[a] {
			return false
		}
	}
	if len(r.Assign) > 10000 {
		return false
	}
	for copyRel, a := range r.Assign {
		if !seen[a] || !safeRel(copyRel) {
			return false
		}
		if _, _, ok := conflict.Parse(filepath.Base(filepath.FromSlash(copyRel))); !ok {
			return false
		}
	}
	return true
}

// safeRel accepts a relative path that stays inside its folder.
func safeRel(rel string) bool {
	if rel == "" || filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(filepath.FromSlash(rel)) != "" ||
		strings.ContainsAny(rel, ":\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

// Clean drops invalid entries (the local file could have been edited by
// hand) and caps the active log.
func (s *State) Clean() {
	var acc []Account
	for _, a := range s.Accounts {
		if validAccount(a) {
			acc = append(acc, a)
		}
	}
	s.Accounts = acc
	var rec []Record
	for _, r := range s.Records {
		if ValidRecord(r) {
			rec = append(rec, r)
		}
	}
	s.Records = rec
	if len(s.ActiveLog) > maxActiveLog {
		s.ActiveLog = s.ActiveLog[len(s.ActiveLog)-maxActiveLog:]
	}
}

// ValidLog is a PC's published active log, checked and in order.
func (s Shared) ValidLog() []ActiveEntry {
	var out []ActiveEntry
	for _, e := range s.ActiveLog {
		if ValidAccountID(e.Account) && !e.Since.IsZero() {
			out = append(out, e)
		}
	}
	if len(out) > maxActiveLog {
		out = out[len(out)-maxActiveLog:]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// ActiveAt says which account was active at t according to a PC's log, or "".
func ActiveAt(log []ActiveEntry, t time.Time) string {
	who := ""
	for _, e := range log {
		if e.Since.After(t) {
			break
		}
		who = e.Account
	}
	return who
}

// FolderLabel is the Syncthing label of an account's folder, e.g.
// "Stardew Valley (Anna)": it names that account's backup too.
func FolderLabel(label, name string) string {
	if name == "" {
		return label
	}
	return label + " (" + name + ")"
}

// ---- editing -------------------------------------------------------------------

// ErrInUse means an account still owns saves of a split game.
var ErrInUse = errors.New("this account still has its own saves of a split game; merge those games first")

// NewAccountID returns a fresh random account id.
func NewAccountID(taken func(string) bool) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	for {
		b := make([]byte, 6)
		r := randomBytes(6)
		for i := range b {
			b[i] = alphabet[int(r[i])%len(alphabet)]
		}
		if id := string(b); !taken(id) {
			return id
		}
	}
}

// CleanName trims and checks an account name.
func CleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", errors.New("enter a name")
	}
	if len(name) > maxName {
		return "", errors.New("that name is too long")
	}
	return name, nil
}

// ValidColor accepts "" or #rrggbb.
func ValidColor(c string) bool { return c == "" || colorPattern.MatchString(c) }

// InUse reports whether any split game gives account its own folder.
func (s Shared) InUse(account string) bool {
	for _, r := range s.Records {
		if r.Kind == KindSplit && r.Has(account) {
			return true
		}
	}
	return false
}

// SetActive records that account is now the active one on this PC.
func (s *State) SetActive(account string, now time.Time) {
	if s.Active == account && len(s.ActiveLog) > 0 {
		return
	}
	s.Active = account
	s.ActiveLog = append(s.ActiveLog, ActiveEntry{Account: account, Since: now})
	if len(s.ActiveLog) > maxActiveLog {
		s.ActiveLog = s.ActiveLog[len(s.ActiveLog)-maxActiveLog:]
	}
}

// Put stores rec as the game's latest record.
func (s *State) Put(rec Record) {
	for i, r := range s.Records {
		if r.Game == rec.Game {
			s.Records[i] = rec
			return
		}
	}
	s.Records = append(s.Records, rec)
}

// EnsureMember gives account its own (empty) folder in every split game
// that doesn't have one for it yet, e.g. an account created on one PC while
// another split a game. It reports whether any record changed.
func (s *State) EnsureMember(account, by string, now time.Time) bool {
	if _, ok := s.Get(account); !ok {
		return false
	}
	changed := false
	for _, r := range s.Splits() {
		if r.Has(account) || len(r.Accounts) >= MaxAccounts {
			continue
		}
		r.Accounts = append(append([]string(nil), r.Accounts...), account)
		r.Fresh = append(append([]string(nil), r.Fresh...), account)
		r.Origin = r.Seeder()
		r.ParentGen = r.Gen
		r.Gen, r.Created, r.By = r.Gen+1, now, by
		s.Put(r)
		changed = true
	}
	return changed
}

// NextGen is the Gen a new record for game gets.
func (s Shared) NextGen(game string) int {
	if r, ok := s.Record(game); ok {
		return r.Gen + 1
	}
	return 1
}

// Pending lists the records this PC hasn't carried out yet.
func (s State) Pending() []Record {
	var out []Record
	for _, r := range s.Records {
		if s.Applied[r.Game] != r.Stamp() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Game < out[j].Game })
	return out
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
