package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// A game's exclusions are file patterns (Syncthing's .stignore syntax) that
// this PC neither syncs nor backs up. They're kept in Syncer's settings by
// save folder, and written into a synced folder's .stignore between Syncer's
// markers, leaving the user's own lines alone.

func init() { meta.BeforeAdd = writeExclusions }

const maxExclusions = 100

// syncIgnores are never synced, in any folder. steam_autocloud.vdf belongs
// to the PC it's on: Steam writes it for the account using that PC, and a
// copy arriving from another PC names the wrong owner, to Steam and to
// Syncer's Steam Cloud check alike.
var syncIgnores = []string{backup.SteamMarker}

// withSyncIgnores returns the lines of Syncer's .stignore block for a game
// with the exclusions pats.
func withSyncIgnores(pats []string) []string {
	out := append([]string(nil), syncIgnores...)
	for _, p := range pats {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, p) }) {
			out = append(out, p)
		}
	}
	return out
}

// cleanExclusions validates patterns typed by the user.
func cleanExclusions(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > 200 {
			return nil, fmt.Errorf("%q… is too long", p[:30])
		}
		if strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("%q has characters that can't be used", p)
		}
		if strings.HasPrefix(p, "#") || strings.HasPrefix(p, "!") || strings.HasPrefix(p, "//") {
			return nil, fmt.Errorf("%q: patterns can't start with #, ! or //", p)
		}
		bare := p
		for _, pre := range []string{"(?i)", "(?d)"} {
			bare = strings.TrimPrefix(bare, pre)
		}
		switch strings.Trim(bare, `/\`) {
		case "", "*", "**":
			return nil, fmt.Errorf("%q would skip every file", p)
		}
		if k := strings.ToLower(p); !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	if len(out) > maxExclusions {
		return nil, fmt.Errorf("at most %d patterns", maxExclusions)
	}
	return out, nil
}

// mergeIgnores replaces Syncer's block in a .stignore's lines with patterns
// (none = no block) and keeps every other line.
func mergeIgnores(current, patterns []string) []string {
	var out []string
	managed := false
	for _, l := range current {
		switch strings.TrimSpace(l) {
		case backup.IgnoreBegin:
			managed = true
			continue
		case backup.IgnoreEnd:
			if managed {
				managed = false
				continue
			}
		}
		if !managed {
			out = append(out, l)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(patterns) > 0 {
		out = append(out, backup.IgnoreBegin)
		out = append(out, patterns...)
		out = append(out, backup.IgnoreEnd)
	}
	return out
}

// writeExclusions puts Syncer's lines (the game's exclusions and
// syncIgnores) into its folder's .stignore just before Syncthing starts on
// the folder, so its first scan already skips them.
func writeExclusions(_, path string) {
	p := filepath.Join(path, ".stignore")
	cur := readIgnores(p)
	next := mergeIgnores(cur, withSyncIgnores(store.LoadSettings().Exclude[dismissKey(path)]))
	if slices.Equal(cur, next) {
		return
	}
	if err := os.WriteFile(p, []byte(strings.Join(next, "\n")+"\n"), 0o644); err != nil {
		logx.Printf("write exclusions for %s: %v", path, err)
	}
}

// readIgnores reads a .stignore's lines (none if it doesn't exist).
func readIgnores(p string) []string {
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.ReplaceAll(strings.TrimRight(string(b), "\r\n"), "\r\n", "\n"), "\n")
}

// ensureIgnores brings Syncer's .stignore lines up to date in every synced
// folder, including ones added before a line existed.
func ensureIgnores(ctx context.Context, c *syncthing.Client) {
	fs, err := c.Folders(ctx)
	if err != nil {
		return
	}
	ex := store.LoadSettings().Exclude
	failed := 0
	for _, f := range fs {
		if f.ID == meta.FolderID {
			continue
		}
		if err := applyExclusions(ctx, c, f.ID, withSyncIgnores(ex[dismissKey(f.Path)])); err != nil {
			failed++
		}
	}
	if failed > 0 {
		logx.Printf("couldn't update the ignore list of %d synced folder(s)", failed)
	}
}

// applyExclusions updates a synced folder's .stignore through Syncthing, which
// rescans with the new list right away.
func applyExclusions(ctx context.Context, c *syncthing.Client, id string, pats []string) error {
	cur, err := c.Ignores(ctx, id)
	if err != nil {
		return err
	}
	next := mergeIgnores(cur, pats)
	if slices.Equal(cur, next) {
		return nil
	}
	return c.SetIgnores(ctx, id, next)
}

// SetExclusions sets which files of a game this PC skips (patterns like
// "*.log" or "Screenshots"), for both syncing and the backup.
func (a *App) SetExclusions(id string, patterns []string) error {
	pats, err := cleanExclusions(patterns)
	if err != nil {
		return err
	}
	f, err := folderByID(id)
	if err != nil {
		return err
	}
	key := dismissKey(f.Path)
	if _, err := store.UpdateSettings(func(s *store.Settings) {
		if len(pats) == 0 {
			delete(s.Exclude, key)
		} else {
			s.Exclude[key] = pats
		}
	}); err != nil {
		return err
	}
	logx.Printf("exclusions of %s: %s", f.Label, cmpOr(strings.Join(pats, ", "), "none"))
	defer runtime.EventsEmit(a.ctx, "changed")
	if _, backupOnly := store.LoadSettings().BackupOnly[id]; backupOnly {
		return nil
	}
	c, err := a.client()
	if err == nil {
		ctx, cancel := a.callCtx()
		defer cancel()
		err = applyExclusions(ctx, c, id, withSyncIgnores(pats))
	}
	if c == nil || errors.Is(err, syncthing.ErrNotRunning) {
		writeExclusions(id, f.Path) // Syncthing reads it when it starts
		return nil
	}
	if err != nil {
		return fmt.Errorf("saved for the backup, but sync couldn't be updated: %w", err)
	}
	return nil
}
