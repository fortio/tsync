// Package syncdir is the (network independent) logic of tsync's directory sync between 2 peers: scanning
// a directory into an index, the ignore rules, the base (what both sides had at the last sync) and deciding
// for each path what to do. Each side only ever writes its own disk (it pulls what the peer has newer), and
// nothing modified on both sides since the last sync is touched (conflict, left for the user to resolve).
package syncdir

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fortio.org/progressbar"
)

// Action is what to do for a path, from our side (the peer decides the mirror action on its side).
type Action int

const (
	InSync       Action = iota // same content on both sides (or absent on both): nothing to do.
	Pull                       // new or changed there (and not here): get the peer's version.
	MetadataOnly               // same content, the peer changed the mode (we didn't): set its mode.
	PeerPulls                  // new or changed here (and not there): nothing to do here, the peer pulls it.
	Conflict                   // changed on both sides (or file vs directory): touch neither.
	DeletedHere                // deleted here, unchanged there: not propagated (to be shown).
	DeletedThere               // deleted there, unchanged here: not propagated (to be shown).
)

func (a Action) String() string {
	switch a {
	case InSync:
		return "InSync"
	case Pull:
		return "Pull"
	case MetadataOnly:
		return "MetadataOnly"
	case PeerPulls:
		return "PeerPulls"
	case Conflict:
		return "Conflict"
	case DeletedHere:
		return "DeletedHere"
	case DeletedThere:
		return "DeletedThere"
	default:
		return "Action(" + strconv.Itoa(int(a)) + ")"
	}
}

// sameContent returns true if both are absent, or both are directories, or files with the same hash.
func sameContent(dir1 bool, hash1 *Hash, dir2 bool, hash2 *Hash) bool {
	if hash1 == nil || hash2 == nil {
		return hash1 == nil && hash2 == nil
	}
	return dir1 == dir2 && (dir1 || *hash1 == *hash2)
}

// content returns the parts of an entry (nil if absent) that matter for deciding.
func (e *Entry) content() (bool, *Hash) {
	if e == nil {
		return false, nil
	}
	return e.Dir, &e.Hash
}

func (b *BaseEntry) content() (bool, *Hash) {
	if b == nil {
		return false, nil
	}
	return b.Dir, &b.Hash
}

// Decide returns what to do for a path given our (local) entry, the peer's (remote) one and the base (what
// both had at the last sync), each nil if absent. Content (existence, file vs directory, hash) decides first;
// the mode only matters when the content is the same (see decideMode). Directory times are ignored.
func Decide(local, remote *Entry, base *BaseEntry) Action {
	lDir, lHash := local.content()
	rDir, rHash := remote.content()
	bDir, bHash := base.content()
	unchangedHere := sameContent(lDir, lHash, bDir, bHash)
	unchangedThere := sameContent(rDir, rHash, bDir, bHash)
	switch {
	case sameContent(lDir, lHash, rDir, rHash):
		return decideMode(local, remote, base)
	case local != nil && remote != nil && local.Dir != remote.Dir:
		return Conflict
	case base == nil: // never synced: new on one side, or on both (with different content).
		switch {
		case local == nil:
			return Pull
		case remote == nil:
			return PeerPulls
		default:
			return Conflict
		}
	case unchangedHere && remote == nil:
		return DeletedThere
	case unchangedHere:
		return Pull
	case unchangedThere && local == nil:
		return DeletedHere
	case unchangedThere:
		return PeerPulls
	// Changed on both sides since the base.
	case local == nil: // deleted here, modified there: the modified version wins (nothing lost).
		return Pull
	case remote == nil: // modified here, deleted there.
		return PeerPulls
	default:
		return Conflict
	}
}

// decideMode decides for a path with the same content on both sides: whoever changed the mode since the base
// wins; when that can't be told (no base, content changed since, or both changed it) each side keeps its own
// (a mode difference alone isn't worth a conflict).
func decideMode(local, remote *Entry, base *BaseEntry) Action {
	if local == nil || local.Mode == remote.Mode || base == nil {
		return InSync
	}
	bDir, bHash := base.content()
	lDir, lHash := local.content()
	if !sameContent(lDir, lHash, bDir, bHash) {
		return InSync
	}
	switch base.Mode {
	case local.Mode:
		return MetadataOnly
	case remote.Mode:
		return PeerPulls
	default:
		return InSync
	}
}

// Item is the decision for one path (Local and Remote are nil when absent on that side).
type Item struct {
	Path   string
	Action Action
	Local  *Entry
	Remote *Entry
}

// Plan decides for all the paths of both sides (sorted). Remote paths we ignore (ig, nil for the default rules)
// are left out. Paths that only differ by case are conflicts (they can't coexist on a case insensitive
// file system, eg macOS and Windows) unless both sides have all of them (so both are case sensitive).
// The remote entries' paths must have been validated (see [Key]).
func Plan(local, remote map[string]Entry, base Base, ig *Ignore) []Item {
	if ig == nil {
		ig = DefaultIgnore()
	}
	paths := make([]string, 0, len(local)+len(remote))
	for p := range local {
		paths = append(paths, p)
	}
	for p, e := range remote {
		if _, ok := local[p]; !ok && !ig.Match(p, e.Dir) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	byFold := make(map[string][]string)
	for _, p := range paths {
		f := strings.ToLower(p)
		byFold[f] = append(byFold[f], p)
	}
	items := make([]Item, 0, len(paths))
	for _, p := range paths {
		var it Item
		it.Path = p
		if e, ok := local[p]; ok {
			it.Local = &e
		}
		if e, ok := remote[p]; ok && !ig.Match(p, e.Dir) {
			it.Remote = &e
		}
		var b *BaseEntry
		if e, ok := base[p]; ok {
			b = &e
		}
		it.Action = Decide(it.Local, it.Remote, b)
		if caseConflict(byFold[strings.ToLower(p)], local, remote) {
			it.Action = Conflict
		}
		items = append(items, it)
	}
	return items
}

// caseConflict returns true if there are several paths differing only by case and not all on both sides.
func caseConflict(group []string, local, remote map[string]Entry) bool {
	if len(group) < 2 {
		return false
	}
	for _, p := range group {
		_, l := local[p]
		_, r := remote[p]
		if !l || !r {
			return true
		}
	}
	return false
}

// numActions is the number of actions.
const numActions = int(DeletedThere) + 1

// maxSummaryConflicts is how many conflicting paths a [Summary] keeps.
const maxSummaryConflicts = 5

// Summary counts the items of a plan by action.
type Summary struct {
	Counts    [numActions]int
	PullBytes int64    // size of the files to pull.
	Conflicts []string // the first conflicting paths.
}

// Summarize counts the items by action.
func Summarize(items []Item) Summary {
	var s Summary
	for _, it := range items {
		s.Counts[it.Action]++
		switch it.Action {
		case Pull:
			if !it.Remote.Dir {
				s.PullBytes += it.Remote.Size
			}
		case Conflict:
			if len(s.Conflicts) < maxSummaryConflicts {
				s.Conflicts = append(s.Conflicts, it.Path)
			}
		default:
		}
	}
	return s
}

// Pending returns how many paths aren't in sync.
func (s Summary) Pending() int {
	n := 0
	for a, c := range s.Counts {
		if Action(a) != InSync {
			n += c
		}
	}
	return n
}

// Equal returns true if both summaries are the same.
func (s Summary) Equal(o Summary) bool {
	return s.Counts == o.Counts && s.PullBytes == o.PullBytes && slices.Equal(s.Conflicts, o.Conflicts)
}

// Describe describes the summary (eg "3 to get (1.2 Mb) · 1 for peer · 1 conflict"), "in sync (N items)"
// (files and directories) when there is nothing to do.
func (s Summary) Describe(peerName string) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+what)
		}
	}
	if n := s.Counts[Pull]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d to get (%s)", n, progressbar.HumanBytes(s.PullBytes)))
	}
	add(s.Counts[MetadataOnly], "mode changes to get")
	add(s.Counts[PeerPulls], "for "+peerName)
	add(s.Counts[Conflict], plural(s.Counts[Conflict], "conflict"))
	add(s.Counts[DeletedHere], "deleted here")
	add(s.Counts[DeletedThere], "deleted on "+peerName)
	if len(parts) == 0 {
		n := s.Counts[InSync]
		return fmt.Sprintf("in sync (%d %s)", n, plural(n, "item"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
