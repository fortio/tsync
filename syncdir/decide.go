// Package syncdir is the (network independent) logic of tsync's directory sync between 2 peers: scanning
// a directory into an index, the ignore rules, the base (what both sides had at the last sync) and deciding
// for each path what to do. Each side only ever writes its own disk (it pulls what the peer has newer, removes
// what the peer deleted), and nothing modified on both sides since the last sync is overwritten (conflict).
package syncdir

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"fortio.org/progressbar"
)

// Action is what to do for a path, from our side (the peer decides the mirror action on its side).
type Action int

const (
	InSync           Action = iota // same content on both sides (or absent on both): nothing to do.
	Pull                           // new or changed there (and not here): get the peer's version.
	MetadataOnly                   // same content, the peer changed the mode (we didn't): set its mode.
	PeerPulls                      // new or changed here (and not there): nothing to do here, the peer pulls it.
	Conflict                       // changed on both sides (or file vs directory): touch neither.
	DeletedHere                    // deleted here, unchanged there: nothing to do here, the peer removes it.
	DeletedThere                   // deleted there, unchanged here: remove it (moved to OldDir).
	DeletionHeld                   // DeletedThere but among too many at once (eg peer's disk not mounted): not applied.
	ConflictCopy                   // conflict (see PlanOptions) we lose: ours renamed to Copy, theirs (if any) got.
	PeerConflictCopy               // conflict the peer loses: nothing to do here, the peer makes the conflict copy.
	Move                           // moved there (same content deleted at a path, new at another, see detectMoves): From to Path.
	MovedAway                      // the From of a Move: nothing to do (the move takes care of it).
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
	case DeletionHeld:
		return "DeletionHeld"
	case ConflictCopy:
		return "ConflictCopy"
	case PeerConflictCopy:
		return "PeerConflictCopy"
	case Move:
		return "Move"
	case MovedAway:
		return "MovedAway"
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
	Copy   string // ConflictCopy: the path our version is renamed to.
	From   *Entry // Move: our entry (unchanged since the last sync) to rename to Path.
}

// PlanOptions are the rules and policies of a [Plan].
type PlanOptions struct {
	Ignore       *Ignore // ours, nil for the default rules.
	RemoteIgnore *Ignore // the peer's, nil for the default rules.
	// ConflictCopies resolves the conflicts between files (and modified vs deleted) instead of leaving them:
	// the newest version keeps the name, the other one is renamed to a conflict copy (see ConflictCopyName),
	// on the side that has it (see ConflictCopy), so it is synced like any other file.
	ConflictCopies bool
	Name           string // our name, in the name of the conflict copies we make.
}

// Plan decides for all the paths of both sides (sorted). Remote paths we ignore are left out, and ours the
// peer ignores aren't deletions there nor for it to get: they're in sync. Conflicts are resolved with conflict
// copies if opts says so. Paths that only differ by case are conflicts (they can't coexist on a case
// insensitive file system, eg macOS and Windows) unless both sides have all of them (so both are case
// sensitive). Files moved there are moved here (see detectMoves). When the peer deleted too many paths at
// once (see massDeletion) they are held, not removed. The remote entries' paths must have been validated
// (see [ValidKey]).
func Plan(local, remote map[string]Entry, base Base, opts PlanOptions) []Item {
	ig, remoteIg := opts.Ignore, opts.RemoteIgnore
	if ig == nil {
		ig = DefaultIgnore()
	}
	if remoteIg == nil {
		remoteIg = DefaultIgnore()
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
		if it.Remote == nil && it.Local != nil && remoteIg.Match(p, it.Local.Dir) {
			it.Action = InSync // the peer doesn't sync it (not a deletion there).
		}
		if opts.ConflictCopies {
			resolveConflict(&it, b, opts.Name)
		}
		if caseConflict(byFold[strings.ToLower(p)], local, remote) {
			it.Action, it.Copy = Conflict, ""
		}
		items = append(items, it)
	}
	detectMoves(items, base)
	holdMassDeletion(items, len(base))
	return items
}

// resolveConflict changes conflicts between files, and files modified on one side but deleted on the other,
// to ConflictCopy for the side that has the version to keep as a copy (the older one, or the modified one so
// the deletion still applies to the name) and PeerConflictCopy for the other side.
func resolveConflict(it *Item, base *BaseEntry, ourName string) {
	l, r := it.Local, it.Remote
	switch {
	case it.Action == Conflict && l != nil && r != nil && !l.Dir && !r.Dir:
		if weLose(l, r) {
			it.Action, it.Copy = ConflictCopy, ConflictCopyName(it.Path, ourName, l.MTime)
		} else {
			it.Action = PeerConflictCopy
		}
	case base == nil || (l != nil && l.Dir) || (r != nil && r.Dir):
		// nothing else to resolve (new on one side, or directories).
	case it.Action == PeerPulls && r == nil && l != nil: // modified here (else it would be DeletedThere), deleted there.
		it.Action, it.Copy = ConflictCopy, ConflictCopyName(it.Path, ourName, l.MTime)
	case it.Action == Pull && l == nil && r != nil: // deleted here, modified there.
		it.Action = PeerConflictCopy
	}
}

// weLose returns true if our version is the one to keep as a conflict copy: the older one (by modification
// time, the peer decides the same as it compares the same 2 versions), or the lower hash for the same time.
func weLose(local, remote *Entry) bool {
	if !local.MTime.Equal(remote.MTime) {
		return local.MTime.Before(remote.MTime)
	}
	return slices.Compare(local.Hash[:], remote.Hash[:]) < 0
}

// ConflictCopyMarker is in the name of conflict copies (see [ConflictCopyName]).
const ConflictCopyMarker = ".sync-conflict-"

// ConflictCopyName returns the name of the conflict copy of the version of path from host, modified at mtime:
// "dir/name.sync-conflict-<host>-<UTC time>.ext" (same name for the same version, on any machine).
func ConflictCopyName(p, host string, mtime time.Time) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // eg ".bashrc".
		stem, ext = name, ""
	}
	host = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, host)
	return dir + stem + ConflictCopyMarker + host + "-" + mtime.UTC().Format("20060102-150405") + ext
}

// IsConflictCopy returns true if the path is a conflict copy (see [ConflictCopyName]).
func IsConflictCopy(p string) bool {
	return strings.Contains(path.Base(p), ConflictCopyMarker)
}

// detectMoves changes the files new there (Pull, never synced) with the same content (hash and size) as one
// deleted there (DeletedThere) into a Move of ours (and MovedAway for the deleted one): no transfer needed,
// and they're not deletions (eg a directory renamed there).
func detectMoves(items []Item, base Base) {
	type content struct {
		hash Hash
		size int64
	}
	deleted := make(map[content][]int)
	for i, it := range items {
		if it.Action == DeletedThere && !it.Local.Dir {
			c := content{it.Local.Hash, it.Local.Size}
			deleted[c] = append(deleted[c], i)
		}
	}
	if len(deleted) == 0 {
		return
	}
	for i := range items {
		it := &items[i]
		if it.Action != Pull || it.Local != nil || it.Remote.Dir {
			continue
		}
		if _, synced := base[it.Path]; synced {
			continue
		}
		c := content{it.Remote.Hash, it.Remote.Size}
		if from := deleted[c]; len(from) > 0 {
			it.Action, it.From = Move, items[from[0]].Local
			items[from[0]].Action = MovedAway
			deleted[c] = from[1:]
		}
	}
}

// massDeletion is how many paths deleted at once on the peer, if also more than half of the ones both had,
// are held (likely its directory isn't mounted or was emptied by mistake, rather than deliberate deletions).
const massDeletion = 20

// holdMassDeletion holds the deletions there if there are too many (see massDeletion).
func holdMassDeletion(items []Item, synced int) {
	n := 0
	for _, it := range items {
		if it.Action == DeletedThere {
			n++
		}
	}
	if n <= massDeletion || 2*n <= synced {
		return
	}
	for i := range items {
		if items[i].Action == DeletedThere {
			items[i].Action = DeletionHeld
		}
	}
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
const numActions = int(MovedAway) + 1

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
	add(s.Counts[Move], "to move")
	add(s.Counts[MetadataOnly], "mode changes to get")
	add(s.Counts[PeerPulls], "for "+peerName)
	add(s.Counts[Conflict], plural(s.Counts[Conflict], "conflict"))
	add(s.Counts[DeletedThere], "to remove")
	add(s.Counts[DeletedHere], plural(s.Counts[DeletedHere], "removal")+" for "+peerName)
	add(s.Counts[DeletionHeld], plural(s.Counts[DeletionHeld], "deletion")+" on "+peerName+" held (too many at once)")
	add(s.Counts[ConflictCopy], copies(s.Counts[ConflictCopy])+" to make")
	add(s.Counts[PeerConflictCopy], copies(s.Counts[PeerConflictCopy])+" for "+peerName+" to make")
	if len(parts) == 0 {
		n := s.Counts[InSync]
		return fmt.Sprintf("in sync (%d %s)", n, plural(n, "item"))
	}
	return strings.Join(parts, " · ")
}

func copies(n int) string {
	if n == 1 {
		return "conflict copy"
	}
	return "conflict copies"
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
