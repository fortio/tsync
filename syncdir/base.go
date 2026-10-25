package syncdir

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// BaseEntry is what both sides had for a path at the last sync.
type BaseEntry struct {
	Dir  bool
	Mode fs.FileMode // ModeUnknown when the sides had different modes.
	Hash Hash        // files only.
}

// ModeUnknown is the base mode when both sides had the same content but different modes: neither mode is
// then propagated (each side keeps its own) until one changes.
const ModeUnknown = fs.ModeIrregular

// Base is the state at the last sync with a peer (one per synced directory and peer), by key.
type Base map[string]BaseEntry

// Record records e (the same on both sides) as synced.
func (b Base) Record(e Entry) {
	b[e.Path] = BaseEntry{Dir: e.Dir, Mode: e.Mode, Hash: e.Hash}
}

// baseHeader is the first line of a base file (format version).
const baseHeader = "tsyncbase1"

// LoadBase reads a base saved by [Base.Save]. A missing file is an empty base (never synced).
func LoadBase(file string) (Base, error) {
	b := make(Base)
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	line := 0
	for sc.Scan() {
		line++
		if line == 1 {
			if sc.Text() != baseHeader {
				return nil, fmt.Errorf("%s: not a tsync base file (or newer version): %q", file, sc.Text())
			}
			continue
		}
		key, e, perr := parseBaseLine(sc.Text())
		if perr != nil {
			return nil, fmt.Errorf("%s line %d: %w", file, line, perr)
		}
		b[key] = e
	}
	return b, sc.Err()
}

// parseBaseLine parses `f|d <octal mode> <hex hash>|- <quoted path>`.
func parseBaseLine(line string) (string, BaseEntry, error) {
	var e BaseEntry
	fields := strings.SplitN(line, " ", 4)
	if len(fields) != 4 || (fields[0] != "f" && fields[0] != "d") {
		return "", e, errors.New("invalid line")
	}
	e.Dir = fields[0] == "d"
	if fields[1] == "?" {
		e.Mode = ModeUnknown
	} else {
		mode, err := strconv.ParseUint(fields[1], 8, 32)
		if err != nil || mode > 0o777 {
			return "", e, fmt.Errorf("invalid mode %q", fields[1])
		}
		e.Mode = fs.FileMode(mode)
	}
	if e.Dir != (fields[2] == "-") {
		return "", e, fmt.Errorf("invalid hash %q", fields[2])
	}
	if !e.Dir {
		h, herr := hex.DecodeString(fields[2])
		if herr != nil || len(h) != len(e.Hash) {
			return "", e, fmt.Errorf("invalid hash %q", fields[2])
		}
		copy(e.Hash[:], h)
	}
	key, err := strconv.Unquote(fields[3])
	if err != nil || key == "" {
		return "", e, fmt.Errorf("invalid path %s", fields[3])
	}
	return key, e, nil
}

// Save writes the base to file (atomically, see [WriteFileAtomic]).
func (b Base) Save(file string) error {
	return WriteFileAtomic(file, func(w *bufio.Writer) {
		_, _ = w.WriteString(baseHeader + "\n")
		for _, key := range slices.Sorted(maps.Keys(b)) {
			e := b[key]
			kind, hash := "f", e.Hash.String()
			if e.Dir {
				kind, hash = "d", "-"
			}
			mode := strconv.FormatUint(uint64(e.Mode.Perm()), 8)
			if e.Mode == ModeUnknown {
				mode = "?"
			}
			_, _ = fmt.Fprintf(w, "%s %s %s %s\n", kind, mode, hash, strconv.Quote(key))
		}
	})
}

// WriteFileAtomic writes file with what write writes: to a temporary file in the same directory, renamed to
// file once complete (so it is never seen partially written), creating the directory if needed.
func WriteFileAtomic(file string, write func(w *bufio.Writer)) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(file)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed.
	w := bufio.NewWriter(tmp)
	write(w)
	err = w.Flush()
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// UpdateBase records the paths in sync in the plan (same content on both sides; with ModeUnknown if their
// modes differ, unless the base already has that content) and forgets the ones absent on both sides.
// Returns true if the base changed.
func UpdateBase(base Base, items []Item) bool {
	changed := false
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		seen[it.Path] = true
		if it.Action != InSync || it.Local == nil || it.Remote == nil {
			continue
		}
		want := BaseEntry{Dir: it.Local.Dir, Mode: it.Local.Mode, Hash: it.Local.Hash}
		b, ok := base[it.Path]
		if it.Local.Mode != it.Remote.Mode {
			if ok && b.Dir == want.Dir && b.Hash == want.Hash {
				continue // keep the mode we know (see decideMode).
			}
			want.Mode = ModeUnknown
		}
		if !ok || b != want {
			base[it.Path] = want
			changed = true
		}
	}
	for p := range base {
		if !seen[p] {
			delete(base, p)
			changed = true
		}
	}
	return changed
}
