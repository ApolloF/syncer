package backup

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Matcher applies Syncthing-style ignore patterns (from .stignore) plus
// Syncthing's own bookkeeping files, so the backup mirrors what gets synced.
type Matcher struct {
	res []*regexp.Regexp
}

var builtin = []string{".stfolder", ".stversions", ".stignore", "~syncthing~*", ".syncthing.*.tmp", "*.syncer-tmp", "desktop.ini", "Thumbs.db"}

// The lines Syncer writes into a synced folder's .stignore (a game's
// exclusions) sit between these markers; the user's own lines are kept.
const (
	IgnoreBegin = "// Syncer: begin (edit these in Syncer)"
	IgnoreEnd   = "// Syncer: end"
)

// LoadMatcher reads <dir>/.stignore if present, plus extra patterns (the
// game's exclusions). Syncer's own block in .stignore is skipped: extra is
// the current list, the block may be left over from when it was synced.
func LoadMatcher(dir string, extra ...string) *Matcher {
	pats := append([]string(nil), builtin...)
	if f, err := os.Open(filepath.Join(dir, ".stignore")); err == nil {
		sc := bufio.NewScanner(f)
		managed := false
		for sc.Scan() {
			switch l := sc.Text(); strings.TrimSpace(l) {
			case IgnoreBegin:
				managed = true
			case IgnoreEnd:
				managed = false
			default:
				if !managed {
					pats = append(pats, l)
				}
			}
		}
		f.Close()
	}
	return NewMatcher(append(pats, extra...))
}

// NewMatcher compiles patterns. Negations and #include lines are skipped
// (over-including in a backup is the safe direction).
func NewMatcher(pats []string) *Matcher {
	m := &Matcher{}
	for _, p := range pats {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "!") {
			continue
		}
		for {
			switch {
			case strings.HasPrefix(p, "(?d)"), strings.HasPrefix(p, "(?i)"):
				p = p[4:]
				continue
			}
			break
		}
		p = strings.TrimSuffix(strings.ReplaceAll(p, `\`, "/"), "/")
		anchored := strings.HasPrefix(p, "/")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			continue
		}
		re := globRE(p)
		if anchored {
			re = "^" + re + "(/.*)?$"
		} else {
			re = "(^|/)" + re + "(/.*)?$"
		}
		if r, err := regexp.Compile("(?i)" + re); err == nil {
			m.res = append(m.res, r)
		}
	}
	return m
}

// Ignored reports whether rel (forward or back slashes) is excluded.
func (m *Matcher) Ignored(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, r := range m.res {
		if r.MatchString(rel) {
			return true
		}
	}
	return false
}

func globRE(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
