package meta

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// ApplyAccounts, when set, carries out split and merge records this PC
// hasn't yet (and finishes an interrupted one). Reconcile calls it before it
// looks at the folders.
var ApplyAccounts func(ctx context.Context, c *syncthing.Client, me string, others []string) error

// combineAccounts takes in the accounts and split games other PCs published.
func combineAccounts(me string) {
	var peers []accounts.Shared
	for _, df := range readOtherFiles(me) {
		if df.Accounts != nil {
			peers = append(peers, *df.Accounts)
		}
	}
	if len(peers) == 0 {
		return
	}
	cur := accounts.Load()
	if !cur.Combine(peers) { // cheap check without the lock first
		return
	}
	if _, err := accounts.Update(func(s *accounts.State) error {
		if s.Combine(peers) {
			logx.Printf("accounts: took in changes from other PCs")
		}
		return nil
	}); err != nil {
		logx.Printf("accounts: %v", err)
	}
}

// PeerAccounts returns what each other PC published about accounts (device
// id -> data): who plays there, and since when.
func PeerAccounts(me string) map[string]accounts.Shared {
	m := map[string]accounts.Shared{}
	for _, df := range readOtherFiles(me) {
		if df.Accounts != nil {
			m[df.Device] = *df.Accounts
		}
	}
	return m
}

// WithoutFeature lists the paired PCs (by device id) whose Syncer doesn't
// publish feature yet, or hasn't published anything.
func WithoutFeature(devices []string, feature string) []string {
	has := map[string]bool{}
	for _, df := range readDeviceFiles() {
		for _, f := range df.Features {
			if f == feature {
				has[df.Device] = true
			}
		}
	}
	// A PC whose metadata hasn't arrived yet may run an older Syncer, so it
	// counts too. (A device without Syncer, e.g. a NAS, has to be removed
	// under Devices, or its folders kept, before splitting.)
	var out []string
	for _, d := range devices {
		if !has[d] {
			out = append(out, d)
		}
	}
	return out
}

// accountSkip is why an account folder (id) of split game, which other PCs
// sync, is deliberately not synced here ("" if it may be, or the game isn't
// known as split here yet). have are the folders Syncthing has.
func accountSkip(ast accounts.State, id, game string, have map[string]bool, s store.Settings,
	installed func(string) bool, synced []string) string {
	if s.Ignored[id] {
		return SkipRemoved
	}
	r, ok := ast.Split(game)
	if !ok || haveGame(have, game) {
		return ""
	}
	_, reason := Adoptable(SharedFolder{ID: game, Label: r.Label, Root: r.Root, Rel: r.Rel}, s, installed, synced)
	return reason
}

func isSplit(game string) bool {
	_, ok := accounts.Load().Split(game)
	return ok
}

// haveGame reports whether any folder of game (shared or per account) is in have.
func haveGame(have map[string]bool, game string) bool {
	for id := range have {
		if id == game {
			return true
		}
		if g, _, ok := accounts.ParseFolderID(id); ok && g == game {
			return true
		}
	}
	return false
}

// accountRoot marks the root of a published account folder (see Reconcile).
const accountRoot = "account:"

// splitPlace turns a folder of a split game into the folder this PC keeps at
// the game's save folder: the active account's. An account folder of a game
// this PC doesn't know as split becomes the game's shared folder: one
// person's saves are never joined at the save folder by guesswork.
func splitPlace(sf SharedFolder) SharedFolder {
	game := sf.ID
	if g, _, ok := accounts.ParseFolderID(sf.ID); ok {
		game = g
	}
	st := accounts.Load()
	r, ok := st.Split(game)
	if !ok {
		if game != sf.ID {
			return SharedFolder{ID: game, Label: sf.Label, Root: strings.TrimPrefix(sf.Root, accountRoot), Rel: sf.Rel}
		}
		return sf
	}
	a := accounts.PlaceLive(r, st.ActiveID())
	return SharedFolder{ID: accounts.FolderID(game, a), Label: r.Label, Root: r.Root, Rel: r.Rel, Account: a}
}

type folderAdd struct{ id, label, path string }

// accountFolders decides which per-account folders of split games to add
// here: all accounts of a game this PC already syncs (the missing ones go to
// the vault), and for a game this PC doesn't sync yet, the same rules as for
// any folder from another PC, with the active account's saves at the save
// folder.
func accountFolders(ast accounts.State, cands []SharedFolder, byID map[string]syncthing.Folder, s store.Settings,
	installed func(string) bool, synced []string) []folderAdd {
	games := map[string][]string{} // game -> account ids offered by other PCs
	for _, sf := range cands {
		g, a, ok := accounts.ParseFolderID(sf.ID)
		if !ok {
			continue
		}
		if _, ok := byID[sf.ID]; ok {
			continue
		}
		if r, ok := ast.Split(g); !ok || !r.Has(a) {
			continue // merged again, or not known here yet
		}
		games[g] = append(games[g], a)
	}
	if _, busy := accounts.PendingOp(); busy {
		return nil // an operation is moving these folders; next time
	}
	var names []string
	for g := range games {
		if r, _ := ast.Split(g); ast.Applied[g] != r.Stamp() {
			continue // this PC hasn't carried the split out yet
		}
		names = append(names, g)
	}
	sort.Strings(names)
	var out []folderAdd
	for _, g := range names {
		r, _ := ast.Split(g)
		live, ok := r.LivePath()
		if !ok || s.Ignored[g] {
			continue
		}
		have, atLive := false, false
		for id, f := range byID {
			if fg, _, ok := accounts.ParseFolderID(id); ok && fg == g {
				have = true
				if samePath(f.Path, live) {
					atLive = true
				}
			}
			if id == g {
				// The shared folder is still here: the split hasn't been
				// carried out on this PC yet (ApplyAccounts does that).
				have, atLive = true, true
			}
		}
		if _, ok := byID[g]; ok {
			continue
		}
		if !have {
			check := SharedFolder{ID: g, Label: r.Label, Root: r.Root, Rel: r.Rel}
			if _, reason := Adoptable(check, s, installed, synced); reason != "" {
				continue
			}
		}
		place := accounts.PlaceLive(r, ast.ActiveID())
		accs := games[g]
		sort.SliceStable(accs, func(i, j int) bool { return accs[i] == place && accs[j] != place })
		for _, a := range accs {
			id := accounts.FolderID(g, a)
			if s.Ignored[id] {
				continue
			}
			path := accounts.VaultDir(live, a, g)
			if a == place && !atLive {
				path, atLive = live, true
			} else {
				// A folder already in the vault holds this account's saves
				// from before (merging empties the vault): it's joined, and
				// protected by a restore point first like any existing saves.
				if err := os.MkdirAll(path, 0o755); err != nil {
					logx.Printf("accounts: %s: %v", path, err)
					continue
				}
			}
			out = append(out, folderAdd{id: id, label: accounts.FolderLabel(r.Label, ast.Name(a)), path: path})
		}
	}
	return out
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// RefreshCache records the current folder list for backups made while
// Syncthing is down (Reconcile does too, but not while syncing is paused).
func RefreshCache(ctx context.Context, c *syncthing.Client) {
	fs, err := c.Folders(ctx)
	if err != nil {
		return
	}
	byID := map[string]syncthing.Folder{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	cacheFolders(byID)
}
