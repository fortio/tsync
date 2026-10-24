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
	"slices"
	"strings"
	"sync"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/syncdir"
)

// Directory sync (dry run for now: nothing is written): each side scans its sync folders periodically, tells
// its linked peers the names of its folders when the link is up, and sends its index (files and directories
// with their hash) of the folders they have in common, again when it changes. Each side then decides what
// sync would do for each path (see [syncdir.Plan]) and shows it.

const (
	FrameSyncFolders = 'F' // our sync folder names, newline separated (when the link is up).
	// FrameSyncIndex is a chunk of our index of a folder: name length (1) + name + flags (1) + entries:
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
	local  syncdir.Index   // last scan (Time is zero until the first one).
	ig     *syncdir.Ignore // rules of the last scan.
	err    string          // last scan error.
	peers  map[Peer]*syncPeer
}

// syncPeer is the state of a folder with a linked peer that has it too.
type syncPeer struct {
	l       *link
	remote  map[string]syncdir.Entry // the peer's last complete index.
	pending map[string]syncdir.Entry // chunks being received.
	ready   bool                     // remote is set.
	base    syncdir.Base
	summary syncdir.Summary
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
		}
		if len(f.peers) == 0 {
			res = append(res, st)
			continue
		}
		for _, peer := range slices.SortedFunc(maps.Keys(f.peers), func(a, b Peer) int { return strings.Compare(a.Name, b.Name) }) {
			sp := f.peers[peer]
			st.Peer, st.Ready, st.Summary = peer, sp.ready, sp.summary
			res = append(res, st)
		}
	}
	return res
}

// startSync starts scanning our sync folders.
func (s *Server) startSync(ctx context.Context) {
	for _, sf := range s.Sync {
		f := &syncFolder{SyncFolder: sf, peers: make(map[Peer]*syncPeer)}
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
	for {
		ig, err := syncdir.LoadIgnore(f.Path)
		var idx syncdir.Index
		if err == nil {
			idx, err = syncdir.Scan(f.Path, prev, ig)
		}
		if err == nil {
			prev = idx
		}
		for _, t := range s.syncScanned(f, ig, idx, err) {
			s.sendIndex(f, t.peer, t.l, t.conn)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
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
	if !f.local.Time.IsZero() && maps.EqualFunc(f.local.Entries, idx.Entries, sameEntry) {
		f.local.Time = idx.Time
		return nil
	}
	if f.local.Time.IsZero() {
		log.Infof("Sync folder %q (%s): %d files and directories (%d skipped)", f.Name, f.Path, len(idx.Entries), idx.Skipped)
	}
	f.local, f.ig = idx, ig
	for peer, sp := range f.peers {
		s.replanLocked(f, peer, sp)
	}
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
	if err := l.send(conn, FrameSyncFolders, []byte(strings.Join(names, "\n"))); err != nil {
		log.Errf("Failed to send sync folders to %s: %v", peer.Name, err)
	}
}

// sendIndex sends our (last scanned) index of the folder to the peer, in chunks.
func (s *Server) sendIndex(f *syncFolder, peer Peer, l *link, conn net.Conn) {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()
	s.stateMu.Lock()
	entries := f.local.Entries // not modified once scanned (a new scan makes a new map).
	s.stateMu.Unlock()
	chunks := indexChunks(f.Name, entries, maxSyncChunk)
	for _, c := range chunks {
		if err := l.send(conn, FrameSyncIndex, c); err != nil {
			log.Errf("Failed to send sync folder %q index to %s: %v", f.Name, peer.Name, err)
			return
		}
	}
	log.LogVf("Sent sync folder %q index (%d entries, %d frames) to %s", f.Name, len(entries), len(chunks), peer.Name)
}

// indexChunks returns the FrameSyncIndex payloads for the entries (sorted), each with at most maxChunk bytes
// of entries (at least one entry per chunk, and one chunk for an empty index).
func indexChunks(name string, entries map[string]syncdir.Entry, maxChunk int) [][]byte {
	header := append([]byte{byte(len(name))}, name...) //nolint:gosec // validated, at most MaxSyncNameLen.
	flagsAt := len(header)
	var chunks [][]byte
	cur := append(slices.Clone(header), syncIndexFirst)
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
		s.syncFoldersReceived(peer, l, conn, string(plain[1:]))
	case FrameSyncIndex:
		if err := s.syncIndexReceived(peer, l, plain[1:]); err != nil {
			log.Errf("Invalid sync index from %s: %v", peer.Name, err)
		}
	default:
		return false
	}
	return true
}

// syncFoldersReceived records the peer's sync folders and sends it our index of the ones we have too.
func (s *Server) syncFoldersReceived(peer Peer, l *link, conn net.Conn, names string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	l.syncFolders = make(map[string]bool)
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
	name, flags := string(b[1:1+b[0]]), b[1+b[0]]
	entries, err := decodeEntries(b[2+b[0]:])
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	f := s.syncFolders[name]
	if f == nil || s.links[peer] != l {
		log.LogVf("Ignoring sync index of %q from %s", name, peer.Name)
		return nil
	}
	sp := f.peers[peer]
	if sp == nil || sp.l != l {
		sp = &syncPeer{l: l, base: s.loadBaseLocked(f, peer)}
		f.peers[peer] = sp
	}
	if flags&syncIndexFirst != 0 {
		sp.pending = make(map[string]syncdir.Entry)
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
	sp.remote, sp.pending, sp.ready = sp.pending, nil, true
	log.LogVf("Received sync folder %q index from %s (%d entries)", name, peer.Name, len(sp.remote))
	s.replanLocked(f, peer, sp)
	s.change(0)
	return nil
}

// loadBaseLocked loads the base of the folder with that peer (empty if never synced or without storage).
func (s *Server) loadBaseLocked(f *syncFolder, peer Peer) syncdir.Base {
	if s.Storage == nil {
		return syncdir.Base{}
	}
	file := filepath.Join(s.Storage.Dir, "sync", f.Name, peer.PublicKey+".base")
	b, err := syncdir.LoadBase(file)
	if err != nil {
		log.Errf("Can't load the sync base of %q with %s: %v", f.Name, peer.Name, err)
		return syncdir.Base{}
	}
	return b
}

// replanLocked decides what sync would do for the folder with the peer, logs it if it changed.
func (s *Server) replanLocked(f *syncFolder, peer Peer, sp *syncPeer) {
	if f.local.Time.IsZero() || !sp.ready {
		return
	}
	sum := syncdir.Summarize(syncdir.Plan(f.local.Entries, sp.remote, sp.base, f.ig))
	if sum.Equal(sp.summary) {
		return
	}
	sp.summary = sum
	log.Infof("Sync %q with %s (dry run): %s", f.Name, peer.Name, sum.Describe(peer.Name))
	if len(sum.Conflicts) > 0 {
		log.Warnf("Sync %q with %s: conflicts (changed on both sides): %q", f.Name, peer.Name, sum.Conflicts)
	}
}

// syncLinkClosedLocked forgets the peers' state of a closed link.
func (s *Server) syncLinkClosedLocked(l *link) {
	for _, f := range s.syncFolders {
		for peer, sp := range f.peers {
			if sp.l == l {
				delete(f.peers, peer)
			}
		}
	}
}
