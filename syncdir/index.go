package syncdir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Hash is the sha256 of a file content.
type Hash [sha256.Size]byte

func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

// Entry is a file or directory of a synced directory.
type Entry struct {
	Path  string      // key (see [Key]).
	Dir   bool        // directory (Size and Hash aren't set).
	Mode  fs.FileMode // permissions, except group/other write (see keptMode).
	Size  int64
	MTime time.Time
	Hash  Hash
	// diskPath is the slash separated path as on disk when it differs from Path (not normalized).
	diskPath string
	// racy is set when the file was hashed less than racyWindow after it was modified: it could have been
	// modified again since with the same size and time, so it is hashed again on the next scan.
	racy bool
}

// DiskPath returns the (slash separated, relative) path of the entry as on disk: what to open, which can
// differ from Path (the key) by Unicode normalization.
func (e *Entry) DiskPath() string {
	if e.diskPath != "" {
		return e.diskPath
	}
	return e.Path
}

// KeptMode is the mode tracked and set for a file or directory (see [Entry.Mode]).
func KeptMode(mode fs.FileMode) fs.FileMode {
	return keptMode(mode)
}

// Index is the scan of a synced directory.
type Index struct {
	Entries map[string]Entry // by key (see [Key]).
	Time    time.Time        // when the scan started.
	Skipped int              // symlinks, special or unreadable files, name duplicates once normalized.
}

// racyWindow is how recent a file modification time can be, when hashed, for the hash to not be reused on the
// next scan even if the size and time didn't change (it could have been modified again within the time
// granularity of the file system, up to 2s for FAT, after we read it).
const racyWindow = 2 * time.Second

// Key returns the index key of a (relative) path: slash separated and NFC normalized (macOS can give
// decomposed, NFD, names, eg for accented letters, while other systems usually have NFC ones).
func Key(path string) string {
	return norm.NFC.String(filepath.ToSlash(path))
}

// Scan scans the directory root (not following symlinks) into an index, skipping what ig ignores (nil for
// the default rules). Files are only hashed if new or changed since prev (zero for none): different size or
// modification time, or modified too close to when they were hashed (see racyWindow).
func Scan(root string, prev Index, ig *Ignore) (Index, error) {
	if ig == nil {
		ig = DefaultIgnore()
	}
	idx := Index{Entries: make(map[string]Entry), Time: time.Now()}
	r, err := os.OpenRoot(root)
	if err != nil {
		return idx, err
	}
	defer r.Close()
	_, err = scanTree(r, ".", &idx, prev, ig, nil)
	return idx, err
}

// scanTree adds to idx the entries of the tree at p (slash separated, relative to the root, as on disk; "."
// for the whole root, which isn't added itself), returns the keys of its directories (p's included).
// Files in force are hashed again even if their size and time didn't change.
func scanTree(r *os.Root, p string, idx *Index, prev Index, ig *Ignore, force map[string]bool) ([]string, error) {
	var dirs []string
	err := fs.WalkDir(r.FS(), p, func(p string, d fs.DirEntry, werr error) error {
		if p == "." {
			return werr
		}
		if werr != nil { // unreadable directory: already listed (called again after it failed to be read).
			idx.Skipped++
			return fs.SkipDir
		}
		key := Key(p)
		if ig.Match(key, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !addEntry(idx, r, p, key, d, prev.entry(key), force[key]) {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, key)
		}
		return nil
	})
	return dirs, err
}

// addEntry adds the entry for p (key) to idx, returns false if skipped (special file, unreadable, duplicate key).
func addEntry(idx *Index, r *os.Root, p, key string, d fs.DirEntry, prev *Entry, force bool) bool {
	if force {
		prev = nil
	}
	e, ok := scanEntry(r, p, d, prev)
	if _, dup := idx.Entries[key]; !ok || dup {
		idx.Skipped++
		return false
	}
	e.Path = key
	if p != key {
		e.diskPath = p
	}
	idx.Entries[key] = e
	return true
}

// ScanDirs is a partial [Scan]: prev updated for what changed in the given directories (keys, "" for the root),
// eg the ones file system notifications said changed: their direct content is read again (files in force are
// hashed again even if their size and time didn't change), new sub directories are scanned whole, removed ones
// are removed with all they had. Returns the updated index and the new directories (keys, to watch).
func ScanDirs(root string, prev Index, dirs []string, ig *Ignore, force map[string]bool) (Index, []string, error) {
	if ig == nil {
		ig = DefaultIgnore()
	}
	idx := Index{Entries: maps.Clone(prev.Entries), Time: time.Now(), Skipped: prev.Skipped}
	r, err := os.OpenRoot(root)
	if err != nil {
		return idx, nil, err
	}
	defer r.Close()
	dirty := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		dirty[d] = true
	}
	gone := make(map[string]bool) // directories (children of dirty ones) not seen again (yet).
	for k, e := range idx.Entries {
		if dirty[parentKey(k)] {
			delete(idx.Entries, k)
			if e.Dir {
				gone[k] = true
			}
		}
	}
	var added []string
	for _, d := range dirs {
		p := "."
		if d != "" {
			e, ok := prev.Entries[d]
			if !ok || !e.Dir {
				continue // new (found by scanning its parent) or not a directory anymore.
			}
			p = e.DiskPath()
		}
		children, rerr := fs.ReadDir(r.FS(), p)
		if rerr != nil {
			continue // gone (its parent should be dirty too) or unreadable.
		}
		for _, c := range children {
			cp := path.Join(p, c.Name())
			key := Key(cp)
			if ig.Match(key, c.IsDir()) || !addEntry(&idx, r, cp, key, c, prev.entry(key), force[key]) || !c.IsDir() {
				continue
			}
			if gone[key] {
				delete(gone, key) // still there, its content (if not dirty itself) is unchanged.
				continue
			}
			// New directory: all of it.
			delete(idx.Entries, key)
			newDirs, serr := scanTree(r, cp, &idx, prev, ig, force)
			if serr != nil {
				return idx, added, serr
			}
			added = append(added, newDirs...)
		}
	}
	if len(gone) > 0 { // removed directories: and everything they had.
		for k := range idx.Entries {
			if inAny(k, gone) {
				delete(idx.Entries, k)
			}
		}
	}
	return idx, added, nil
}

// parentKey returns the key of the directory containing key ("" for the root).
func parentKey(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return ""
}

// inAny returns true if one of key's parent directories is in dirs.
func inAny(key string, dirs map[string]bool) bool {
	for p := parentKey(key); p != ""; p = parentKey(p) {
		if dirs[p] {
			return true
		}
	}
	return false
}

func (idx Index) entry(key string) *Entry {
	e, ok := idx.Entries[key]
	if !ok {
		return nil
	}
	return &e
}

// scanEntry returns the entry for the directory or regular file at p, false for anything else (symlinks...)
// or if it can't be read.
func scanEntry(r *os.Root, p string, d fs.DirEntry, prev *Entry) (Entry, bool) {
	fi, err := d.Info() // not following symlinks.
	// On Windows the listing's time of a new directory can be stale.
	if err == nil && fi.IsDir() {
		fi, err = r.Lstat(filepath.FromSlash(p))
	}
	if err != nil || (!fi.IsDir() && !fi.Mode().IsRegular()) {
		return Entry{}, false
	}
	e := Entry{Dir: fi.IsDir(), Mode: scannedMode(fi.Mode(), prev), MTime: fi.ModTime()}
	if e.Dir {
		return e, true
	}
	e.Size = fi.Size()
	if prev != nil && !prev.Dir && prev.Size == e.Size && prev.MTime.Equal(e.MTime) && !prev.racy {
		e.Hash = prev.Hash
		return e, true
	}
	h, size, mtime, err := hashFile(r, p)
	if err != nil {
		return Entry{}, false
	}
	e.Hash, e.Size, e.MTime, e.racy = h, size, mtime, time.Since(mtime) <= racyWindow
	return e, true
}

// hashFile returns the hash of the file and its size and time when opened (if it changes while being read,
// the next scan sees a different size or time and hashes it again).
func hashFile(r *os.Root, p string) (Hash, int64, time.Time, error) {
	var h Hash
	f, err := r.Open(filepath.FromSlash(p))
	if err != nil {
		return h, 0, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return h, 0, time.Time{}, err
	}
	if !fi.Mode().IsRegular() { // replaced since listed.
		return h, 0, time.Time{}, errors.New("not a regular file")
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, f); err != nil {
		return h, 0, time.Time{}, err
	}
	copy(h[:], hasher.Sum(nil))
	return h, fi.Size(), fi.ModTime(), nil
}

// keptMode is the mode we keep track of (and set): permissions, except group and other write (Windows
// reports all files as writable by all, and a peer's umask may differ).
func keptMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() &^ 0o022
}

// scannedMode is the mode we keep track of for what we scanned. Windows only has a read only attribute (Go
// reports 0666 or 0444): the other bits come from the previous scan (or base) so a mode set by the peer (eg
// executable) isn't seen as changed here.
func scannedMode(mode fs.FileMode, prev *Entry) fs.FileMode {
	mode = keptMode(mode)
	if runtime.GOOS == "windows" && prev != nil {
		mode = prev.Mode&^0o200 | mode&0o200
	}
	return mode
}

// maxDepth limits the depth of paths (received from a peer).
const maxDepth = 64

// ValidKey checks a key received from a peer: what [Key] returns for a relative path inside the synced
// directory (no empty, "." or ".." parts, no backslash or control characters, limited depth and length).
func ValidKey(key string) error {
	if key == "" || len(key) > 0xffff || Key(key) != key {
		return errors.New("invalid or not normalized path")
	}
	parts := strings.Split(key, "/")
	if len(parts) > maxDepth {
		return errors.New("path too deep")
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsRune(p, '\\') || strings.ContainsFunc(p, unicode.IsControl) {
			return errors.New("invalid path")
		}
	}
	return nil
}

// ValidWindowsKey checks that a key can be a path on Windows: no part with one of <>:"|?* (eg "a:b" would be
// an alternate data stream of "a"), ending with a dot or a space (Windows drops them), or a reserved device
// name (CON, NUL, COM1..., with or without an extension).
func ValidWindowsKey(key string) error {
	for part := range strings.SplitSeq(key, "/") {
		if strings.ContainsAny(part, `<>:"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("invalid name on windows")
		}
		base := strings.ToUpper(part)
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		switch base = strings.TrimSpace(base); {
		case base == "CON" || base == "PRN" || base == "AUX" || base == "NUL",
			len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
				base[3] >= '0' && base[3] <= '9':
			return errors.New("reserved device name on windows")
		}
	}
	return nil
}
