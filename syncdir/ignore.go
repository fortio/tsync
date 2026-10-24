package syncdir

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const (
	// IgnoreFile is the ignore rules file at the root of a synced directory. Without one, hidden files and
	// directories (name starting with a dot) are ignored; with one, only what it lists is (it is synced
	// itself unless it lists itself).
	IgnoreFile = ".tsyncignore"
	// TrashDir is where replaced versions are kept (at the root of a synced directory), never synced.
	TrashDir = ".tsync-trash"
	// partialSuffix ends the name of the temporary files of downloads in progress, never synced.
	partialSuffix = ".tsync-partial"
)

// Ignore is the set of rules for which paths aren't synced.
type Ignore struct {
	custom bool // from an IgnoreFile, else the default: hidden files and directories.
	rules  []rule
}

// rule is a .tsyncignore line, a subset of the .gitignore syntax: `*`, `?` and `[...]` within a name, `**`
// for any number of directories, a trailing `/` for directories only, and a pattern with a `/` (other than a
// trailing one, eg `/top` or `a/b`) is matched from the root while one without (eg `*.log`) matches a name at
// any level. Matching a directory ignores everything in it.
type rule struct {
	parts    []string // slash separated parts of the pattern.
	anchored bool
	dirOnly  bool
}

// DefaultIgnore returns the default rules (no IgnoreFile): hidden files and directories are ignored,
// except the root IgnoreFile.
func DefaultIgnore() *Ignore {
	return &Ignore{}
}

// ParseIgnore parses the content of an IgnoreFile.
func ParseIgnore(content string) (*Ignore, error) {
	ig := &Ignore{custom: true}
	for i, line := range strings.Split(content, "\n") {
		line = norm.NFC.String(strings.TrimSpace(line))
		if line == "" || line[0] == '#' {
			continue
		}
		r, err := parseRule(line)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", IgnoreFile, i+1, err)
		}
		ig.rules = append(ig.rules, r)
	}
	return ig, nil
}

func parseRule(line string) (rule, error) {
	var r rule
	if line[0] == '!' {
		return r, errors.New("negation (!) is not supported")
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	r.anchored = strings.Contains(line, "/")
	line = strings.TrimLeft(line, "/")
	if line == "" {
		return r, errors.New("empty pattern")
	}
	r.parts = strings.Split(line, "/")
	for _, p := range r.parts {
		if p == "" {
			return r, fmt.Errorf("empty part in %q", line)
		}
		if _, err := path.Match(p, ""); err != nil {
			return r, fmt.Errorf("invalid pattern %q: %w", p, err)
		}
	}
	return r, nil
}

// LoadIgnore returns the rules of the IgnoreFile in root, or the default ones if there is none.
func LoadIgnore(root string) (*Ignore, error) {
	b, err := os.ReadFile(filepath.Join(root, IgnoreFile))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultIgnore(), nil
	}
	if err != nil {
		return nil, err
	}
	return ParseIgnore(string(b))
}

// Match returns true if the path (a key, see [Key]) of a file (or directory if dir) is ignored, including
// because one of its parent directories is.
func (ig *Ignore) Match(key string, dir bool) bool {
	if key == TrashDir || strings.HasPrefix(key, TrashDir+"/") || strings.HasSuffix(key, partialSuffix) {
		return true
	}
	if !ig.custom {
		return key != IgnoreFile && hasDotPart(key)
	}
	parts := strings.Split(key, "/")
	for i := 1; i <= len(parts); i++ {
		isDir := dir || i < len(parts)
		for _, r := range ig.rules {
			if r.match(parts[:i], isDir) {
				return true
			}
		}
	}
	return false
}

func (r rule) match(parts []string, dir bool) bool {
	if r.dirOnly && !dir {
		return false
	}
	if !r.anchored {
		ok, _ := path.Match(r.parts[0], parts[len(parts)-1])
		return ok
	}
	return matchParts(r.parts, parts)
}

// matchParts matches the pattern parts ("**" for any number of parts) to the path parts.
func matchParts(pattern, parts []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := range len(parts) + 1 {
				if matchParts(pattern[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], parts[0]); !ok {
			return false
		}
		pattern, parts = pattern[1:], parts[1:]
	}
	return len(parts) == 0
}

// hasDotPart returns true if a part of the path starts with a dot (hidden).
func hasDotPart(key string) bool {
	for part := range strings.SplitSeq(key, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
