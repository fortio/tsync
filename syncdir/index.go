package syncdir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
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
}

// Index is the scan of a synced directory.
type Index struct {
	Entries map[string]Entry // by key (see [Key]).
	Time    time.Time        // when the scan started.
	Skipped int              // symlinks, special or unreadable files, name duplicates once normalized.
}

// racyWindow is how recent a file modification time can be for the hash to not be reused on the next scan
// even if the size and time didn't change (it could have been modified again within the time granularity of
// the file system, up to 2s for FAT, after we read it).
const racyWindow = 2 * time.Second

// Key returns the index key of a (relative) path: slash separated and NFC normalized (macOS can give
// decomposed, NFD, names, eg for accented letters, while other systems usually have NFC ones).
func Key(path string) string {
	return norm.NFC.String(filepath.ToSlash(path))
}

// Scan scans the directory root (not following symlinks) into an index, skipping what ig ignores (nil for
// the default rules). Files are only hashed if new or changed since prev (zero for none): different size or
// modification time, or a time too close to the previous scan's (see racyWindow).
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
	err = fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
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
		e, ok := scanEntry(r, p, d, prev.entry(key), prev.Time)
		if _, dup := idx.Entries[key]; !ok || dup {
			idx.Skipped++
			return nil
		}
		e.Path = key
		idx.Entries[key] = e
		return nil
	})
	return idx, err
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
func scanEntry(r *os.Root, p string, d fs.DirEntry, prev *Entry, prevTime time.Time) (Entry, bool) {
	fi, err := d.Info() // not following symlinks.
	if err != nil || (!fi.IsDir() && !fi.Mode().IsRegular()) {
		return Entry{}, false
	}
	e := Entry{Dir: fi.IsDir(), Mode: scannedMode(fi.Mode(), prev), MTime: fi.ModTime()}
	if e.Dir {
		return e, true
	}
	e.Size = fi.Size()
	if prev != nil && !prev.Dir && prev.Size == e.Size && prev.MTime.Equal(e.MTime) && prevTime.Sub(e.MTime) > racyWindow {
		e.Hash = prev.Hash
		return e, true
	}
	h, size, mtime, err := hashFile(r, p)
	if err != nil {
		return Entry{}, false
	}
	e.Hash, e.Size, e.MTime = h, size, mtime
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
