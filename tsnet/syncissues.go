package tsnet

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
	lines := make([]string, 0, len(issues)+len(f.copies))
	for _, p := range f.copies {
		e := f.local.Entries[p]
		lines = append(lines, fmt.Sprintf("conflict copy\t%s\there: %s", quoteIfNeeded(p), entryDesc(&e)))
	}
	for _, peer := range slices.SortedFunc(maps.Keys(f.peers), func(a, b Peer) int { return strings.Compare(a.Name, b.Name) }) {
		for _, p := range f.peers[peer].cantStore {
			lines = append(lines, fmt.Sprintf("can't store here\t%s\there: absent\t%s: has it", quoteIfNeeded(p), peer.Name))
		}
	}
	for _, is := range issues {
		kind := "conflict"
		switch is.it.Action {
		case syncdir.DeletionHeld:
			kind = "deletion held"
		case syncdir.CantStoreThere:
			kind = "can't be stored on " + is.peer
		default:
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\there: %s\t%s: %s", kind, quoteIfNeeded(is.it.Path),
			entryDesc(is.it.Local), is.peer, entryDesc(is.it.Remote)))
	}
	return lines
}

// conflictCopies returns the conflict copies of the index (sorted).
func conflictCopies(idx syncdir.Index) []string {
	var res []string
	for p, e := range idx.Entries {
		if !e.Dir && syncdir.IsConflictCopy(p) {
			res = append(res, p)
		}
	}
	slices.Sort(res)
	return res
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
		_, _ = fmt.Fprintf(w, "# tsync: %d things to review in sync folder %q (%s), as of %s.\n",
			len(lines), f.Name, f.Path, time.Now().Format(time.DateTime))
		_, _ = w.WriteString("# conflict copy: the older version of a file changed on both sides (or the modified " +
			"version of a file deleted\n#   on the other side), kept under that name on all machines: delete it " +
			"(on any machine) once reviewed.\n" +
			"# conflict: changed on both sides since the last sync, nothing was touched. Keep one " +
			"version: delete or move away\n#   the other one (on its side) and the remaining one is synced back.\n" +
			"# deletion held: deleted on the peer at the same time as too many others (its folder not mounted?), so " +
			"not done\n#   here: delete it here too if that was intended, or restore it there.\n" +
			"# can't store here (or can't be stored on <peer>): that name isn't valid on this machine (or the peer's),\n" +
			"#   eg on Windows <>:\"|?* or a trailing dot or space, or CON, NUL...: rename it where it is.\n" +
			"# Columns (tab separated): kind, path, our version, the peer's.\n")
		for _, l := range lines {
			_, _ = w.WriteString(l + "\n")
		}
	})
	if err != nil {
		log.Errf("Can't write %s: %v", file, err)
		return
	}
	log.Warnf("Sync %q: %d conflict copies, conflicts or held deletions to review, listed in %s", f.Name, len(lines), file)
}
