package repo

import (
	"bufio"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// IgnoreFileName lists tables that are not versioned, one glob pattern per
// line (# comments allowed). It lives next to .sqit/ and is meant to be
// committed, like .gitignore.
const IgnoreFileName = ".sqitignore"

// IgnoreEnv is a comma-separated list of extra patterns from the
// environment / sqit.env (personal, not committed).
const IgnoreEnv = "SQIT_IGNORE_TABLES"

// IgnoreTemplate is written by `sqit init`.
const IgnoreTemplate = `# Tables that sqit must NOT version, one glob pattern per line.
# Examples:
#   sessions
#   tmp_*
#   *_log
# Personal, uncommitted patterns can go into sqit.env as SQIT_IGNORE_TABLES=a,b,tmp_*
`

// IgnorePatterns returns the patterns from .sqitignore and SQIT_IGNORE_TABLES.
func (r *Repository) IgnorePatterns() ([]string, error) {
	var out []string
	f, err := os.Open(filepath.Join(r.Root, IgnoreFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if i := strings.Index(line, "#"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line != "" {
				out = append(out, line)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	for _, p := range strings.Split(os.Getenv(IgnoreEnv), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Ignored reports whether a table name matches any pattern. Patterns use
// filepath.Match syntax (* ? [...]); a pattern without wildcards is an exact,
// case-insensitive name.
func Ignored(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), lower); ok {
			return true
		}
	}
	return false
}

// SelectTables decides which of the database's tables are tracked: the
// explicit config list if there is one, otherwise every table that is not
// ignored. The result is sorted.
func (r *Repository) SelectTables(available []string) ([]string, error) {
	if len(r.Config.Tables) > 0 {
		out := append([]string(nil), r.Config.Tables...)
		sort.Strings(out)
		return out, nil
	}
	patterns, err := r.IgnorePatterns()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range available {
		if !Ignored(t, patterns) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}
