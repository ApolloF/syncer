package mods

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// Every step that changes deployed mods is checked and the result logged
// (%APPDATA%\Syncer\mod-audit.json), so it's always clear what an update
// did, and a failed check stops syncing that folder until it's sorted out.

// Check is one thing an audit looked at.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"` // not OK, but not a reason to stop
	Detail string `json:"detail,omitempty"`
}

// AuditEntry is one audit.
type AuditEntry struct {
	At      time.Time `json:"at"`
	Folder  string    `json:"folder"`
	Label   string    `json:"label"`
	Phase   string    `json:"phase"` // gate, before, after, rollback, check, source
	Gen     int64     `json:"gen"`
	OK      bool      `json:"ok"`
	Summary string    `json:"summary"`
	Checks  []Check   `json:"checks"`
}

// Passed reports whether no check failed (warnings don't count).
func Passed(cs []Check) bool {
	for _, c := range cs {
		if !c.OK && !c.Warn {
			return false
		}
	}
	return true
}

// Failed lists the checks that failed.
func Failed(cs []Check) []string {
	var out []string
	for _, c := range cs {
		if !c.OK && !c.Warn {
			out = append(out, c.Name+cmpDetail(c.Detail))
		}
	}
	return out
}

func cmpDetail(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

// maxAudit caps the log (a variable for tests).
var maxAudit = 500

var (
	auditMu   sync.Mutex
	auditFile = func() string { return filepath.Join(paths.AppDir(), "mod-audit.json") }
)

// LogAudit appends an entry to the audit log, keeping the newest entries.
func LogAudit(e AuditEntry) {
	auditMu.Lock()
	defer auditMu.Unlock()
	var all []AuditEntry
	if b, err := os.ReadFile(auditFile()); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.OK = Passed(e.Checks)
	all = append(all, e)
	if len(all) > maxAudit {
		all = all[len(all)-maxAudit:]
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := auditFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, auditFile())
	}
}

// AuditLog returns a folder's audit entries, newest first ("" = all folders).
func AuditLog(folder string) []AuditEntry {
	auditMu.Lock()
	defer auditMu.Unlock()
	var all []AuditEntry
	if b, err := os.ReadFile(auditFile()); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	var out []AuditEntry
	for i := len(all) - 1; i >= 0; i-- {
		if folder == "" || all[i].Folder == folder {
			out = append(out, all[i])
		}
	}
	return out
}

// ---- checks -------------------------------------------------------------------

// Stamp is a file's size and modification time.
type Stamp struct {
	Size int64
	Mod  int64
}

const maxOutside = 500_000

// Outside records every file in target that isn't a deployed file (in
// scope): the game's own files, which an update must never change.
func Outside(target string, scope map[string]bool) (map[string]Stamp, error) {
	out := map[string]Stamp{}
	err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == target {
				return err
			}
			return nil
		}
		rel, _ := filepath.Rel(target, p)
		rel = filepath.ToSlash(rel)
		low := strings.ToLower(rel)
		if d.IsDir() {
			if low == ".stfolder" || low == ".stversions" {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(low)
		if scope[low] || low == ".stignore" || isManifestName(base) || base == managedMarker ||
			strings.HasPrefix(base, "~syncthing~") || strings.HasSuffix(base, ".syncer-tmp") {
			return nil
		}
		if len(out) >= maxOutside {
			return fmt.Errorf("more than %d files", maxOutside)
		}
		if fi, err := d.Info(); err == nil {
			out[low] = Stamp{fi.Size(), fi.ModTime().UnixNano()}
		}
		return nil
	})
	return out, err
}

// Scope lists an inventory's files for Outside, by lower-cased path.
func Scope(invs ...*Inventory) map[string]bool {
	m := map[string]bool{}
	for _, inv := range invs {
		if inv == nil {
			continue
		}
		for _, f := range inv.Files {
			m[strings.ToLower(f.Rel)] = true
		}
	}
	return m
}

// CheckOutside compares the game's own files before and after an update.
func CheckOutside(before, after map[string]Stamp) Check {
	c := Check{Name: "Game files untouched", OK: true}
	var changed []string
	for k, b := range before {
		if a, ok := after[k]; !ok || a != b {
			changed = append(changed, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			changed = append(changed, k)
		}
	}
	if len(changed) > 0 {
		c.OK = false
		c.Detail = fmt.Sprintf("%d changed: %s", len(changed), sample(changed))
	} else {
		c.Detail = fmt.Sprintf("%d files checked", len(before))
	}
	return c
}

// CheckApplied checks that target holds exactly the files of inv, and none
// of the removed ones.
func CheckApplied(target string, inv Inventory, removed []string) []Check {
	var missing, wrong, left []string
	for _, f := range inv.Files {
		p := filepath.Join(target, filepath.FromSlash(f.Rel))
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
			missing = append(missing, f.Rel)
		case !sameFile(p, fi, f):
			wrong = append(wrong, f.Rel)
		}
	}
	for _, rel := range removed {
		if _, err := os.Lstat(filepath.Join(target, filepath.FromSlash(rel))); err == nil {
			left = append(left, rel)
		}
	}
	cs := []Check{
		{Name: "Every mod file arrived", OK: len(missing) == 0},
		{Name: "Mod files match the source", OK: len(wrong) == 0},
		{Name: "Removed mod files are gone", OK: len(left) == 0},
	}
	if len(missing) > 0 {
		cs[0].Detail = fmt.Sprintf("%d missing: %s", len(missing), sample(missing))
	} else {
		cs[0].Detail = fmt.Sprintf("%d files", len(inv.Files))
	}
	if len(wrong) > 0 {
		cs[1].Detail = fmt.Sprintf("%d differ: %s", len(wrong), sample(wrong))
	}
	if len(left) > 0 {
		cs[2].Detail = fmt.Sprintf("%d still here: %s", len(left), sample(left))
	}
	return cs
}

// CheckPlugins checks that the plugins game's plugin list names are in
// target (a Bethesda game's Data folder). A missing one is a warning: the
// game skips it.
func CheckPlugins(game, target string) (Check, bool) {
	p := PluginListPath(game, "plugins.txt")
	if p == "" || !strings.EqualFold(filepath.Base(target), "Data") {
		return Check{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Check{Name: "Plugin list", OK: true, Detail: "none"}, true
	}
	var missing []string
	for _, n := range Plugins(string(b)) {
		if _, err := os.Stat(filepath.Join(target, n)); err != nil {
			missing = append(missing, n)
		}
	}
	c := Check{Name: "Plugins in the load order are installed", OK: len(missing) == 0}
	if len(missing) > 0 {
		c.Warn = true
		c.Detail = fmt.Sprintf("%d missing (the game skips them): %s", len(missing), sample(missing))
	}
	return c, true
}

func sample(xs []string) string {
	const n = 5
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}
