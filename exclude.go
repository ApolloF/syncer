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

// writeExclusions puts a game's exclusions into its folder's .stignore just
// before Syncthing starts on the folder, so its first scan skips them.
func writeExclusions(_, path string) {
	pats := store.LoadSettings().Exclude[dismissKey(path)]
	p := filepath.Join(path, ".stignore")
	b, err := os.ReadFile(p)
	if err != nil && len(pats) == 0 {
		return // nothing to write, nothing to clean up
	}
	var cur []string
	if len(b) > 0 {
		cur = strings.Split(strings.ReplaceAll(strings.TrimRight(string(b), "\r\n"), "\r\n", "\n"), "\n")
	}
	if len(pats) == 0 && !slices.ContainsFunc(cur, func(l string) bool { return strings.TrimSpace(l) == backup.IgnoreBegin }) {
		return // the user's own file, nothing of Syncer's in it
	}
	next := mergeIgnores(cur, pats)
	if slices.Equal(cur, next) {
		return
	}
	if len(next) == 0 {
		_ = os.Remove(p)
		return
	}
	if err := os.WriteFile(p, []byte(strings.Join(next, "\n")+"\n"), 0o644); err != nil {
		logx.Printf("write exclusions for %s: %v", path, err)
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
		err = applyExclusions(ctx, c, id, pats)
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
