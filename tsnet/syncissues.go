package tsnet

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fortio.org/log"
	"fortio.org/progressbar"
	"fortio.org/tsync/syncdir"
)

// What isn't resolved automatically, conflicts (with -sync-conflicts list, or file vs directory) and held
// deletions (too many at once), is listed with both sides' versions in a text file per sync folder (outside of
// it, so it isn't synced), for the user to go through. The file is rewritten when the list changes, removed
// when there's nothing left.

// ConflictsFileName is the name of the file listing a sync folder's conflicts and deletions, in the folder's
// directory of our storage (~/.tsync/sync/<label>/).
const ConflictsFileName = "conflicts.txt"

// conflictsFile returns where the folder's conflicts are listed ("" without storage).
func (s *Server) conflictsFile(f *syncFolder) string {
	if s.Storage == nil {
		return ""
	}
	return filepath.Join(s.Storage.Dir, "sync", f.Name, ConflictsFileName)
}

// issueLinesLocked returns the lines (tab separated: kind, path, our version, the peer's) of the conflicts and
// deletions with all the peers, sorted by path (then peer).
func issueLinesLocked(f *syncFolder) []string {
	type issue struct {
		peer string
		it   syncdir.Item
	}
	var issues []issue
	for peer, sp := range f.peers {
		for _, it := range sp.issues {
			issues = append(issues, issue{peer.Name, it})
		}
	}
	slices.SortFunc(issues, func(a, b issue) int {
		if c := strings.Compare(a.it.Path, b.it.Path); c != 0 {
			return c
		}
		return strings.Compare(a.peer, b.peer)
	})
	lines := make([]string, 0, len(issues))
	for _, is := range issues {
		kind := "conflict"
		if is.it.Action == syncdir.DeletionHeld {
			kind = "deletion held"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\there: %s\t%s: %s", kind, quoteIfNeeded(is.it.Path),
			entryDesc(is.it.Local), is.peer, entryDesc(is.it.Remote)))
	}
	return lines
}

// entryDesc describes a version: size and modification time, "directory" or "absent".
func entryDesc(e *syncdir.Entry) string {
	switch {
	case e == nil:
		return "absent"
	case e.Dir:
		return "directory"
	default:
		return progressbar.HumanBytes(e.Size) + ", " + e.MTime.Local().Format(time.DateTime)
	}
}

// quoteIfNeeded quotes a path that has control characters (eg a tab) or quotes, so the lines stay parsable.
func quoteIfNeeded(p string) string {
	if strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || r == '"' }) {
		return strconv.Quote(p)
	}
	return p
}

// writeConflicts (from the folder's goroutine) updates the conflicts file if the list changed: rewritten,
// or removed when there is nothing left.
func (s *Server) writeConflicts(f *syncFolder) {
	file := s.conflictsFile(f)
	if file == "" {
		return
	}
	s.stateMu.Lock()
	lines := issueLinesLocked(f)
	changed := !slices.Equal(lines, f.issueLines)
	f.issueLines = lines
	s.stateMu.Unlock()
	if !changed {
		return
	}
	if len(lines) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Errf("Can't remove %s: %v", file, err)
		}
		return
	}
	err := syncdir.WriteFileAtomic(file, func(w *bufio.Writer) {
		_, _ = fmt.Fprintf(w, "# tsync: %d conflicts or deletions to resolve in sync folder %q (%s), as of %s.\n",
			len(lines), f.Name, f.Path, time.Now().Format(time.DateTime))
		_, _ = w.WriteString("# conflict: changed on both sides since the last sync, nothing was touched. Keep one " +
			"version: delete or move away\n#   the other one (on its side) and the remaining one is synced back.\n" +
			"# deletion held: deleted on the peer at the same time as too many others (its folder not mounted?), so " +
			"not done\n#   here: delete it here too if that was intended, or restore it there.\n" +
			"# Columns (tab separated): kind, path, our version, the peer's.\n")
		for _, l := range lines {
			_, _ = w.WriteString(l + "\n")
		}
	})
	if err != nil {
		log.Errf("Can't write %s: %v", file, err)
		return
	}
	log.Infof("Sync %q: %d conflicts or deletions to resolve, listed in %s", f.Name, len(lines), file)
}
