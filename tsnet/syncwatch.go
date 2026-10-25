package tsnet

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/syncdir"
	"github.com/fsnotify/fsnotify"
)

// Watching a sync folder (see Config.SyncWatch): rather than scanning everything every SyncRescan, file system
// notifications say which directories changed and only those are scanned again (see [syncdir.ScanDirs]), once
// quiet for a bit (they may still be being written). Notifications are only hints: a full scan is still done
// every syncWatchFull, and if it finds changes that weren't notified (eg a network or shared folder) the folder
// goes back to full scans every SyncRescan. Also back to full scans if the directories can't all be watched (eg
// the inotify watches limit on Linux).

const (
	// syncQuiet is how long without notification before the changed directories are scanned again.
	syncQuiet = syncStable
	// syncQuietMax is the longest a change waits to be scanned (notifications keep coming, eg a big copy).
	syncQuietMax = 10 * time.Second
	// syncWatchFull is how often a watched folder is still fully scanned.
	syncWatchFull = 15 * time.Minute
)

// syncWatch is the watching state of a folder, only used by its goroutine (runSync).
type syncWatch struct {
	w       *watcher
	root    string
	watched map[string]bool // directories watched (keys, "" for the root).
	dirty   map[string]bool // directories to scan again.
	force   map[string]bool // files written to (hashed again even with the same size and time).
	since   time.Time       // first notification not scanned yet.
	full    bool            // a full scan is needed (overflow, ignore rules changed).
	events  map[string]bool // directories notified since the last full scan (to detect missed ones).
}

// startWatch watches the folder's directories (after a full scan), returns nil if it can't (polling then).
func (s *Server) startWatch(f *syncFolder, idx syncdir.Index) *syncWatch {
	w, err := newWatcher()
	if err != nil {
		log.Warnf("Sync %q: can't watch for changes (%v), scanning every %v", f.Name, err, s.syncInterval())
		return nil
	}
	sw := &syncWatch{w: w, root: f.Path, watched: make(map[string]bool), events: make(map[string]bool)}
	sw.reset()
	if err = sw.update(idx, nil); err != nil {
		log.Warnf("Sync %q: can't watch all its directories (%v), scanning every %v instead", f.Name, err, s.syncInterval())
		sw.close()
		return nil
	}
	log.Infof("Sync %q: watching %d directories for changes (full scan every %v)", f.Name, len(sw.watched),
		s.syncWatchInterval())
	return sw
}

func (sw *syncWatch) close() {
	if sw != nil {
		_ = sw.w.Close()
	}
}

func (sw *syncWatch) reset() {
	sw.dirty, sw.force, sw.since, sw.full = make(map[string]bool), make(map[string]bool), time.Time{}, false
}

// update watches the directories of idx (all, or only the added ones), stops watching the removed ones.
func (sw *syncWatch) update(idx syncdir.Index, added []string) error {
	dirs := added
	if added == nil {
		dirs = []string{""}
		for k, e := range idx.Entries {
			if e.Dir {
				dirs = append(dirs, k)
			}
		}
	}
	for _, d := range dirs {
		if sw.watched[d] {
			continue
		}
		p := sw.root
		if d != "" {
			e, ok := idx.Entries[d]
			if !ok {
				continue
			}
			p = filepath.Join(sw.root, filepath.FromSlash(e.DiskPath()))
		}
		if err := sw.w.Add(p); err != nil {
			if errors.Is(err, fsnotify.ErrNonExistentWatch) || !isLimit(err) {
				continue // eg already gone.
			}
			return fmt.Errorf("%s: %w", d, err)
		}
		sw.watched[d] = true
	}
	for d := range sw.watched {
		if e, ok := idx.Entries[d]; d != "" && (!ok || !e.Dir) {
			delete(sw.watched, d)
			_ = sw.w.Remove(filepath.Join(sw.root, filepath.FromSlash(d))) // usually already removed with it.
		}
	}
	return nil
}

// isLimit returns true for the errors of a system limit (too many watches or open files).
func isLimit(err error) bool {
	return errors.Is(err, errNoSpace) || errors.Is(err, errTooManyFiles)
}

// event records a notification: the directory of the path (and the path itself if it's a watched directory)
// is to scan again, a written file to hash again.
func (sw *syncWatch) event(ev fsnotify.Event) {
	rel, err := filepath.Rel(sw.root, ev.Name)
	if err != nil {
		return
	}
	key := syncdir.Key(rel)
	if key == "." {
		key = ""
	}
	if key == syncdir.IgnoreFile {
		sw.full = true // the rules changed.
	}
	if key != "" {
		parent := parentDir(key)
		sw.dirty[parent], sw.events[parent] = true, true
	}
	if sw.watched[key] {
		sw.dirty[key], sw.events[key] = true, true
	}
	if ev.Has(fsnotify.Write) {
		sw.force[key] = true
	}
	if sw.since.IsZero() {
		sw.since = time.Now()
	}
}

// quietIn returns when to scan what was notified (zero if nothing).
func (sw *syncWatch) quietIn() time.Duration {
	if sw.since.IsZero() {
		return 0
	}
	return max(min(syncQuiet, syncQuietMax-time.Since(sw.since)), time.Millisecond)
}

// missed returns the paths that changed between 2 full scans (old and idx) in directories that weren't
// notified: the notifications don't work there (eg a network folder).
func (sw *syncWatch) missed(old, idx syncdir.Index) []string {
	var res []string
	check := func(k string) {
		if !sw.events[parentDir(k)] && !sw.events[k] {
			res = append(res, k)
		}
	}
	for k, e := range idx.Entries {
		if o, ok := old.Entries[k]; !ok || !sameEntry(o, e) {
			check(k)
		}
	}
	for k := range old.Entries {
		if _, ok := idx.Entries[k]; !ok {
			check(k)
		}
	}
	slices.Sort(res)
	return res
}

// syncInterval is how often a folder is scanned when not watched.
func (s *Server) syncInterval() time.Duration {
	if s.SyncRescan > 0 {
		return s.SyncRescan
	}
	return DefaultSyncRescan
}

// syncWatchInterval is how often a watched folder is still fully scanned.
func (s *Server) syncWatchInterval() time.Duration {
	return max(syncWatchFull, s.syncInterval())
}
