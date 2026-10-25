package tsnet

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/syncdir"
)

// Directory sync: each side scans its sync folders periodically, tells its linked peers the names of its
// folders when the link is up, and sends its index (files and directories with their hash) of the folders
// they have in common, again when it changes. Each side then decides what to do for each path (see
// [syncdir.Plan]), shows it, and gets what the peer has newer (see syncpull.go).

const (
	// FrameSyncFolders is our current time (unix nano, 8, to check the clocks are close, as conflicts are
	// resolved by modification time) + our sync folder names, newline separated (when the link is up).
	FrameSyncFolders = 'F'
	// FrameSyncIndex is a chunk of our index of a folder: name length (1) + name + flags (1) + (first chunk
	// only: our ignore rules: 1 if from a .tsyncignore (1) + its length (2) + its content) + entries:
	// kind 'f' or 'd' (1) + mode (4) + size (8) + mtime (8) + sha256 (32, files only) + path length (2) + path.
	FrameSyncIndex = 'I'

	syncIndexFirst = 1 // flag of the first chunk of an index.
	syncIndexLast  = 2 // flag of the last chunk.
	// maxSyncChunk is the maximum size of the entries of a FrameSyncIndex.
	maxSyncChunk = 512 * 1024
	// maxRemoteEntries limits the size of an index received from a peer.
	maxRemoteEntries = 1_000_000
	// DefaultSyncRescan is how often sync folders are scanned by default.
	DefaultSyncRescan = 10 * time.Second
	// MaxSyncNameLen is the maximum length of a sync folder name.
	MaxSyncNameLen = 64
	// MaxClockSkew is how much the clocks of 2 peers syncing folders can differ before we warn.
	MaxClockSkew = time.Second
	// syncReplanEvery is how often at most a folder is replanned while syncing (Plan of 100k files: ~70ms).
	syncReplanEvery = time.Second
	// syncSaveEvery is how often at most the bases are saved while syncing (else once done).
	syncSaveEvery = 5 * time.Second
)

// SyncFolder is a directory to sync with the linked peers that have one with the same name.
type SyncFolder struct {
	Name string
	Path string
}

// ValidateSyncName checks a sync folder name: 1 to MaxSyncNameLen letters, digits, '.', '-' or '_', not
// starting with a dot (it is used in file names and in the protocol).
func ValidateSyncName(name string) error {
	if name == "" || len(name) > MaxSyncNameLen || name[0] == '.' ||
		strings.ContainsFunc(name, func(r rune) bool { return !syncNameRune(r) }) {
		return fmt.Errorf("invalid sync folder name %q: 1 to %d letters, digits, '.', '-' or '_' (not starting with '.')",
			name, MaxSyncNameLen)
	}
	return nil
}

func syncNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
}

// syncFolder is the state of one of our sync folders (accessed with stateMu held, except sendMu).
type syncFolder struct {
	SyncFolder
	sendMu sync.Mutex      // one index sent at a time, so its chunks don't interleave with another's.
	ioMu   sync.Mutex      // scans don't see a replacement, move... half done (see syncPull).
	local  syncdir.Index   // last scan (Time is zero until the first one).
	ig     *syncdir.Ignore // rules of the last scan.
	err    string          // last scan error.
	peers  map[Peer]*syncPeer
	// Getting what peers have newer.
	kick     chan struct{}          // wakes up the folder's goroutine (start more pulls, rescan, save).
	inflight map[string]bool        // paths being synced (from any peer).
	failed   map[string]syncFailure // paths that failed (retried after syncRetry).
	recent   map[string]recentEntry // paths we synced, until a scan sees them.
	// Replanned at most every syncReplanEvery while syncing (see runSync).
	replan     bool        // something got synced since the last plan.
	lastReplan time.Time   // when.
	synced     bool        // something got synced since the last scan (rescan once all done, for the peers).
	lastSave   time.Time   // bases saved.
	toSave     []savedBase // bases of peers no longer linked, to save.
	// issueLines are the conflicts and deletions as last written (see writeConflicts).
	issueLines []string
	copies     []string // conflict copies in the folder (from the last scan, sorted).
	// windowsSkips are the peers' paths we can't store (on Windows) and already warned about.
	windowsSkips map[string]bool
}

type savedBase struct {
	file string
	b    syncdir.Base
}

func (f *syncFolder) kickLocked() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// syncPeer is the state of a folder with a linked peer that has it too.
type syncPeer struct {
	l         *link
	remote    map[string]syncdir.Entry // the peer's last complete index.
	remoteIg  *syncdir.Ignore          // and its ignore rules.
	pending   map[string]syncdir.Entry // chunks being received.
	pendingIg *syncdir.Ignore
	ready     bool // remote is set.
	base      syncdir.Base
	summary   syncdir.Summary
	// Getting what the peer has newer.
	todo     []syncdir.Item       // paths to get (or set the mode of...), from the last plan.
	todoAt   int                  // todo before this index is all done (see schedulePullsLocked).
	pulls    map[uint32]*Transfer // downloads in progress.
	baseFile string               // where base is saved ("" without storage).
	dirty    bool                 // base changed since saved.
	issues   []syncdir.Item       // conflicts and deletions, from the last plan.
}

// SyncStatus is the state of a sync folder with a peer (zero Peer if no linked peer has it).
type SyncStatus struct {
	Folder  string
	Path    string
	Peer    Peer
	Files   int    // entries in our last scan.
	Scanned bool   // we scanned it at least once.
	Ready   bool   // we got the peer's index.
	Err     string // last scan error.
	Summary syncdir.Summary
	// Downloads in progress, and the (first) file being downloaded.
	Getting          int
	CurFile          string
	CurDone, CurSize int64
	// ConflictsFile lists the conflicts and deletions, when there are some (and we have storage).
	ConflictsFile string
	// ConflictCopies is how many conflict copies there are in the folder (to review and delete).
	ConflictCopies int
	// ClockSkew is how much the peer's clock is behind ours (ahead if negative), roughly.
	ClockSkew time.Duration
}

// SyncStatus returns the state of our sync folders with each linked peer that has them (sorted).
func (s *Server) SyncStatus() []SyncStatus {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	var res []SyncStatus
	for _, name := range slices.Sorted(maps.Keys(s.syncFolders)) {
		f := s.syncFolders[name]
		st := SyncStatus{
			Folder: f.Name, Path: f.Path, Files: len(f.local.Entries), Scanned: !f.local.Time.IsZero(), Err: f.err,
			ConflictCopies: len(f.copies),
		}
		if len(f.peers) == 0 {
			res = append(res, st)
			continue
		}
		for _, peer := range slices.SortedFunc(maps.Keys(f.peers), func(a, b Peer) int { return strings.Compare(a.Name, b.Name) }) {
			sp := f.peers[peer]
			st.Peer, st.Ready, st.Summary, st.Getting = peer, sp.ready, sp.summary, len(sp.pulls)
			st.CurFile, st.CurDone, st.CurSize, st.ConflictsFile, st.ClockSkew = "", 0, 0, "", sp.l.clockSkew
			if len(sp.issues) > 0 || len(f.copies) > 0 {
				st.ConflictsFile = s.conflictsFile(f)
			}
			if len(sp.pulls) > 0 {
				t := sp.pulls[slices.Min(slices.Collect(maps.Keys(sp.pulls)))]
				st.CurFile, st.CurDone, st.CurSize = t.Source, t.Done, t.Size
			}
			res = append(res, st)
		}
	}
	return res
}

// startSync starts scanning our sync folders.
func (s *Server) startSync(ctx context.Context) {
	for _, sf := range s.Sync {
		f := &syncFolder{
			SyncFolder: sf, peers: make(map[Peer]*syncPeer), kick: make(chan struct{}, 1), inflight: make(map[string]bool),
			failed: make(map[string]syncFailure), recent: make(map[string]recentEntry),
		}
		s.syncFolders[sf.Name] = f
		s.wg.Add(1)
		go s.runSync(ctx, f)
	}
}

func (s *Server) runSync(ctx context.Context, f *syncFolder) {
	defer s.wg.Done()
	interval := s.SyncRescan
	if interval <= 0 {
		interval = DefaultSyncRescan
	}
	var prev syncdir.Index
	timer := time.NewTimer(0) // first scan right away.
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.saveBases(f)
			return
		case <-timer.C:
			prev = s.syncScan(f, prev)
			timer.Reset(interval)
		case <-f.kick:
		}
		s.stateMu.Lock()
		if f.replan && (len(f.inflight) == 0 || time.Since(f.lastReplan) >= syncReplanEvery) {
			s.replanAllLocked(f)
		}
		s.stateMu.Unlock()
		s.startPulls(f)
		s.stateMu.Lock()
		idle := len(f.inflight) == 0 && !f.replan
		rescan := idle && f.synced
		if rescan {
			f.synced = false
		}
		save := idle || time.Since(f.lastSave) >= syncSaveEvery
		retry := interval
		for _, fail := range f.failed { // eg a file still being written there: don't wait for the next scan.
			retry = min(retry, max(time.Until(fail.retryAt()), 100*time.Millisecond))
		}
		if f.replan {
			retry = min(retry, max(syncReplanEvery-time.Since(f.lastReplan), 10*time.Millisecond))
		}
		s.stateMu.Unlock()
		if rescan { // so the peers learn what we got.
			prev = s.syncScan(f, prev)
		}
		if save {
			s.saveBases(f)
		}
		s.writeConflicts(f)
		if retry < interval {
			timer.Reset(retry)
		}
	}
}

// replanAllLocked replans the folder with all the peers, forgets the failures of what's no longer to do.
func (s *Server) replanAllLocked(f *syncFolder) {
	todo := make(map[string]bool)
	for peer, sp := range f.peers {
		s.replanLocked(f, peer, sp)
		for _, it := range sp.todo {
			todo[it.Path] = true
		}
	}
	for p := range f.failed { // no longer to get (eg in sync now, or a conflict).
		if !todo[p] {
			delete(f.failed, p)
		}
	}
	f.replan, f.lastReplan = false, time.Now()
}

// syncScan scans the folder (what we just synced counts as already scanned), sends the index to the
// peers if it changed, returns the scan (for the next one).
func (s *Server) syncScan(f *syncFolder, prev syncdir.Index) syncdir.Index {
	ig, err := syncdir.LoadIgnore(f.Path)
	if err == nil {
		if text, _ := ig.Text(); len(text) > 0xffff { // sent with the index.
			err = fmt.Errorf("%s too large", syncdir.IgnoreFile)
		}
	}
	// What we synced is recorded with ioMu held (see syncPull.record): merged and scanned with it held too.
	f.ioMu.Lock()
	s.stateMu.Lock()
	if len(f.recent) > 0 && prev.Entries != nil {
		prev.Entries = maps.Clone(prev.Entries)
		for k, r := range f.recent {
			if r.deleted {
				delete(prev.Entries, k)
			} else {
				prev.Entries[k] = r.e
			}
		}
	}
	s.stateMu.Unlock()
	var idx syncdir.Index
	if err == nil {
		idx, err = syncdir.Scan(f.Path, prev, ig)
	}
	f.ioMu.Unlock()
	if err == nil {
		prev = idx
	}
	for _, t := range s.syncScanned(f, ig, idx, err) {
		s.sendIndex(f, t.peer, t.l, t.conn)
	}
	return prev
}

type syncTarget struct {
	peer Peer
	l    *link
	conn net.Conn
}

// syncScanned records a scan, returns the linked peers to send the index to if it changed.
func (s *Server) syncScanned(f *syncFolder, ig *syncdir.Ignore, idx syncdir.Index, err error) []syncTarget {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err != nil {
		if f.err != err.Error() {
			log.Errf("Can't scan sync folder %q (%s): %v", f.Name, f.Path, err)
			f.err = err.Error()
			s.change(0)
		}
		return nil
	}
	f.err = ""
	seen := 0 // synced paths this scan saw (started after they were synced).
	for k, r := range f.recent {
		if r.at.Before(idx.Time) {
			delete(f.recent, k)
			seen++
		}
	}
	if !f.local.Time.IsZero() && maps.EqualFunc(f.local.Entries, idx.Entries, sameEntry) {
		f.local.Time = idx.Time
		if seen > 0 {
			s.replanAllLocked(f)
		}
		return nil
	}
	if f.local.Time.IsZero() {
		log.Infof("Sync folder %q (%s): %d files and directories (%d skipped)", f.Name, f.Path, len(idx.Entries), idx.Skipped)
	}
	f.local, f.ig = idx, ig
	copies := conflictCopies(idx)
	if !slices.Equal(copies, f.copies) {
		f.copies = copies
		f.kickLocked() // to update the conflicts list.
	}
	s.replanAllLocked(f)
	s.change(0)
	return s.syncTargetsLocked(f)
}

func sameEntry(a, b syncdir.Entry) bool {
	return a.Path == b.Path && a.Dir == b.Dir && a.Mode == b.Mode && a.Size == b.Size && a.MTime.Equal(b.MTime) && a.Hash == b.Hash
}

// syncTargetsLocked returns the linked peers that have the folder.
func (s *Server) syncTargetsLocked(f *syncFolder) []syncTarget {
	var targets []syncTarget
	for peer, l := range s.links {
		if l.conn != nil && l.syncFolders[f.Name] {
			targets = append(targets, syncTarget{peer, l, l.conn})
		}
	}
	return targets
}

// sendSyncFolders tells a newly linked peer the names of our sync folders.
func (s *Server) sendSyncFolders(peer Peer, l *link, conn net.Conn) {
	if len(s.Sync) == 0 {
		return
	}
	names := make([]string, 0, len(s.Sync))
	for _, f := range s.Sync {
		names = append(names, f.Name)
	}
	payload := binary.BigEndian.AppendUint64(nil, uint64(time.Now().UnixNano()))
	if err := l.send(conn, FrameSyncFolders, append(payload, strings.Join(names, "\n")...)); err != nil {
		log.Errf("Failed to send sync folders to %s: %v", peer.Name, err)
	}
}

// sendIndex sends our (last scanned) index of the folder to the peer, in chunks.
func (s *Server) sendIndex(f *syncFolder, peer Peer, l *link, conn net.Conn) {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()
	s.stateMu.Lock()
	entries, ig := f.local.Entries, f.ig // not modified once scanned (a new scan makes a new map).
	s.stateMu.Unlock()
	chunks := indexChunks(f.Name, entries, ig, maxSyncChunk)
	for _, c := range chunks {
		if err := l.send(conn, FrameSyncIndex, c); err != nil {
			log.Errf("Failed to send sync folder %q index to %s: %v", f.Name, peer.Name, err)
			return
		}
	}
	log.LogVf("Sent sync folder %q index (%d entries, %d frames) to %s", f.Name, len(entries), len(chunks), peer.Name)
}

// indexChunks returns the FrameSyncIndex payloads for the entries (sorted) and ignore rules (nil for the
// default ones), each with at most maxChunk bytes of entries (at least one entry per chunk, and one chunk for
// an empty index).
func indexChunks(name string, entries map[string]syncdir.Entry, ig *syncdir.Ignore, maxChunk int) [][]byte {
	header := append([]byte{byte(len(name))}, name...) //nolint:gosec // validated, at most MaxSyncNameLen.
	flagsAt := len(header)
	var chunks [][]byte
	cur := append(slices.Clone(header), syncIndexFirst)
	rules, custom := "", false
	if ig != nil {
		rules, custom = ig.Text()
	}
	cur = append(cur, 0)
	if custom {
		cur[len(cur)-1] = 1
	}
	cur = append(binary.BigEndian.AppendUint16(cur, uint16(len(rules))), rules...) //nolint:gosec // checked when loaded.
	n := 0
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		e := entries[key]
		if len(e.Path) > 0xffff {
			continue
		}
		if n > 0 && len(cur)-flagsAt-1+entrySize(e) > maxChunk {
			chunks = append(chunks, cur)
			cur = append(slices.Clone(header), 0)
			n = 0
		}
		cur = encodeEntry(cur, e)
		n++
	}
	cur[flagsAt] |= syncIndexLast
	return append(chunks, cur)
}

func entrySize(e syncdir.Entry) int {
	n := 1 + 4 + 8 + 8 + 2 + len(e.Path)
	if !e.Dir {
		n += len(e.Hash)
	}
	return n
}

func encodeEntry(b []byte, e syncdir.Entry) []byte {
	kind := byte('f')
	if e.Dir {
		kind = 'd'
	}
	b = append(b, kind)
	b = binary.BigEndian.AppendUint32(b, uint32(e.Mode))
	b = binary.BigEndian.AppendUint64(b, uint64(e.Size)) //nolint:gosec // positive.
	b = binary.BigEndian.AppendUint64(b, uint64(e.MTime.UnixNano()))
	if !e.Dir {
		b = append(b, e.Hash[:]...)
	}
	b = binary.BigEndian.AppendUint16(b, uint16(len(e.Path))) //nolint:gosec // checked by the caller.
	return append(b, e.Path...)
}

// skipWindowsNames is true on Windows: the peers' paths it can't store are skipped (see filterWindowsNames).
var skipWindowsNames = runtime.GOOS == "windows"

// newWindowsSkips returns the skipped paths not seen before (from any peer), so they're only warned about once.
func (f *syncFolder) newWindowsSkips(skipped []string) []string {
	var added []string
	for _, p := range skipped {
		if !f.windowsSkips[p] {
			if f.windowsSkips == nil {
				f.windowsSkips = make(map[string]bool)
			}
			f.windowsSkips[p] = true
			added = append(added, p)
		}
	}
	return added
}

// filterWindowsNames returns the entries whose paths can be stored on Windows, and the paths of the others.
func filterWindowsNames(entries []syncdir.Entry) ([]syncdir.Entry, []string) {
	var skipped []string
	kept := entries[:0]
	for _, e := range entries {
		if syncdir.ValidWindowsKey(e.Path) != nil {
			skipped = append(skipped, e.Path)
			continue
		}
		kept = append(kept, e)
	}
	return kept, skipped
}

// decodeRules decodes the ignore rules at the start of the first chunk of an index, returns them and the rest.
func decodeRules(b []byte) (*syncdir.Ignore, []byte, error) {
	if len(b) < 3 || len(b) < 3+int(binary.BigEndian.Uint16(b[1:3])) {
		return nil, nil, errors.New("truncated rules")
	}
	n := int(binary.BigEndian.Uint16(b[1:3]))
	if b[0] == 0 {
		return syncdir.DefaultIgnore(), b[3+n:], nil
	}
	ig, err := syncdir.ParseIgnore(string(b[3 : 3+n]))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid ignore rules: %w", err)
	}
	return ig, b[3+n:], nil
}

// decodeEntries decodes (and validates) the entries of a FrameSyncIndex.
func decodeEntries(b []byte) ([]syncdir.Entry, error) {
	var res []syncdir.Entry
	for len(b) > 0 {
		var e syncdir.Entry
		if len(b) < 1+4+8+8 || (b[0] != 'f' && b[0] != 'd') {
			return nil, errors.New("invalid entry")
		}
		e.Dir = b[0] == 'd'
		e.Mode = fs.FileMode(binary.BigEndian.Uint32(b[1:5]))
		e.Size = int64(binary.BigEndian.Uint64(b[5:13]))                 //nolint:gosec // checked below.
		e.MTime = time.Unix(0, int64(binary.BigEndian.Uint64(b[13:21]))) //nolint:gosec // any value is fine.
		b = b[21:]
		if !e.Dir {
			if len(b) < len(e.Hash) {
				return nil, errors.New("truncated entry")
			}
			copy(e.Hash[:], b)
			b = b[len(e.Hash):]
		}
		if len(b) < 2 {
			return nil, errors.New("truncated entry")
		}
		n := int(binary.BigEndian.Uint16(b))
		if len(b) < 2+n || e.Size < 0 {
			return nil, errors.New("invalid entry")
		}
		e.Path = string(b[2 : 2+n])
		b = b[2+n:]
		if err := syncdir.ValidKey(e.Path); err != nil {
			return nil, fmt.Errorf("%w: %q", err, e.Path)
		}
		res = append(res, e)
	}
	return res, nil
}

// handleSyncFrame processes the sync frames (from the link's read loop), returns false for other frame types.
func (s *Server) handleSyncFrame(peer Peer, l *link, conn net.Conn, plain []byte) bool {
	switch plain[0] {
	case FrameSyncFolders:
		if len(plain) < 9 {
			log.Warnf("Invalid sync folders frame from %s", peer.Name)
			return true
		}
		theirs := time.Unix(0, int64(binary.BigEndian.Uint64(plain[1:9]))) //nolint:gosec // any value is fine.
		s.syncFoldersReceived(peer, l, conn, time.Since(theirs), string(plain[9:]))
	case FrameSyncIndex:
		if err := s.syncIndexReceived(peer, l, plain[1:]); err != nil {
			log.Errf("Invalid sync index from %s: %v", peer.Name, err)
		}
	case FrameSyncGet:
		s.syncGetReceived(peer, l, conn, plain[1:])
	default:
		return false
	}
	return true
}

// syncFoldersReceived records the peer's sync folders and sends it our index of the ones we have too.
func (s *Server) syncFoldersReceived(peer Peer, l *link, conn net.Conn, skew time.Duration, names string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	l.syncFolders, l.clockSkew = make(map[string]bool), skew
	if skew > MaxClockSkew || skew < -MaxClockSkew {
		log.Warnf("The clock of %s differs from ours by %v: sync conflicts are resolved by modification time, "+
			"the older version may be the wrong one", peer.Name, skew.Round(time.Millisecond))
	}
	var common []string
	for name := range strings.SplitSeq(names, "\n") {
		if ValidateSyncName(name) != nil {
			continue
		}
		l.syncFolders[name] = true
		if f := s.syncFolders[name]; f != nil {
			common = append(common, name)
			if !f.local.Time.IsZero() { // else sent once scanned.
				s.wg.Go(func() { s.sendIndex(f, peer, l, conn) })
			}
		}
	}
	log.Infof("%s has %d sync folders, %d in common with us %q", peer.Name, len(l.syncFolders), len(common), common)
}

// syncIndexReceived processes a chunk of the peer's index of a folder.
func (s *Server) syncIndexReceived(peer Peer, l *link, b []byte) error {
	if len(b) < 1 || len(b) < 2+int(b[0]) {
		return errors.New("truncated")
	}
	name, flags, b := string(b[1:1+b[0]]), b[1+b[0]], b[2+b[0]:]
	var ig *syncdir.Ignore // the peer's rules (first chunk).
	if flags&syncIndexFirst != 0 {
		var err error
		if ig, b, err = decodeRules(b); err != nil {
			return err
		}
	}
	entries, err := decodeEntries(b)
	if err != nil {
		return err
	}
	var skipped []string
	if skipWindowsNames {
		entries, skipped = filterWindowsNames(entries)
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	f := s.syncFolders[name]
	if f == nil || s.links[peer] != l {
		log.LogVf("Ignoring sync index of %q from %s", name, peer.Name)
		return nil
	}
	if added := f.newWindowsSkips(skipped); len(added) > 0 {
		log.Warnf("Sync %q: %d paths from %s can't be stored on Windows (eg %q), not synced here",
			name, len(added), peer.Name, added[0])
	}
	sp := f.peers[peer]
	if sp == nil || sp.l != l {
		sp = &syncPeer{l: l, pulls: make(map[uint32]*Transfer)}
		sp.base, sp.baseFile = s.loadBaseLocked(f, peer)
		f.peers[peer] = sp
	}
	if flags&syncIndexFirst != 0 {
		sp.pending, sp.pendingIg = make(map[string]syncdir.Entry), ig
	}
	if sp.pending == nil {
		return errors.New("chunk without a first one")
	}
	for _, e := range entries {
		sp.pending[e.Path] = e
	}
	if len(sp.pending) > maxRemoteEntries {
		sp.pending = nil
		return fmt.Errorf("index of %q too large", name)
	}
	if flags&syncIndexLast == 0 {
		return nil
	}
	sp.remote, sp.remoteIg, sp.pending, sp.ready = sp.pending, sp.pendingIg, nil, true
	log.LogVf("Received sync folder %q index from %s (%d entries)", name, peer.Name, len(sp.remote))
	s.replanLocked(f, peer, sp)
	s.change(0)
	return nil
}

// loadBaseLocked loads the base of the folder with that peer (empty if never synced), returns it and its
// file ("" without storage).
func (s *Server) loadBaseLocked(f *syncFolder, peer Peer) (syncdir.Base, string) {
	if s.Storage == nil {
		return syncdir.Base{}, ""
	}
	file := filepath.Join(s.Storage.Dir, "sync", f.Name, peer.PublicKey+".base")
	b, err := syncdir.LoadBase(file)
	if err != nil {
		log.Errf("Can't load the sync base of %q with %s: %v", f.Name, peer.Name, err)
		return syncdir.Base{}, ""
	}
	return b, file
}

// replanLocked decides what to do for the folder with the peer (what we just synced counts as scanned),
// records what is in sync, starts getting what the peer has newer, logs the summary if it changed.
func (s *Server) replanLocked(f *syncFolder, peer Peer, sp *syncPeer) {
	if f.local.Time.IsZero() || !sp.ready {
		return
	}
	local := f.local.Entries
	if len(f.recent) > 0 {
		local = maps.Clone(local)
		for k, r := range f.recent {
			if r.deleted {
				delete(local, k)
			} else {
				local[k] = r.e
			}
		}
	}
	items := syncdir.Plan(local, sp.remote, sp.base, syncdir.PlanOptions{
		Ignore: f.ig, RemoteIgnore: sp.remoteIg, ConflictCopies: !s.SyncConflictList, Name: s.Name,
	})
	if syncdir.UpdateBase(sp.base, items) {
		sp.dirty = true
	}
	sp.todo, sp.todoAt = sp.todo[:0], 0
	hadIssues := len(sp.issues) > 0
	sp.issues = sp.issues[:0]
	for _, it := range items {
		switch it.Action {
		case syncdir.Pull, syncdir.MetadataOnly, syncdir.DeletedThere, syncdir.ConflictCopy, syncdir.Move:
			sp.todo = append(sp.todo, it)
		case syncdir.Conflict, syncdir.DeletionHeld:
			sp.issues = append(sp.issues, it)
		default:
		}
	}
	if len(sp.todo) > 0 || sp.dirty || hadIssues || len(sp.issues) > 0 { // things to get, base to save, list to update.
		f.kickLocked()
	}
	sum := syncdir.Summarize(items)
	if sum.Equal(sp.summary) {
		return
	}
	sp.summary = sum
	log.Infof("Sync %q with %s: %s", f.Name, peer.Name, sum.Describe(peer.Name))
	if len(sum.Conflicts) > 0 {
		log.Warnf("Sync %q with %s: conflicts (changed on both sides): %q", f.Name, peer.Name, sum.Conflicts)
	}
}

// syncLinkClosedLocked forgets the peers' state of a closed link (its downloads were already failed).
func (s *Server) syncLinkClosedLocked(l *link) {
	for _, f := range s.syncFolders {
		for peer, sp := range f.peers {
			if sp.l != l {
				continue
			}
			if sp.dirty && sp.baseFile != "" {
				f.toSave = append(f.toSave, savedBase{sp.baseFile, maps.Clone(sp.base)})
			}
			delete(f.peers, peer)
			f.kickLocked() // to save the base, update the conflicts list.
		}
	}
}
