package tsnet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/syncdir"
)

// Sync pulls: each side only writes its own disk, getting from the peer what it has newer (see
// [syncdir.Decide]). A file is requested by path and expected hash (FrameSyncGet with an id we choose, with
// syncIDBit set so it can't collide with the peer's file offer ids); the peer streams it with the file data
// frames if that's still its current version. Before the downloaded file replaces ours, ours is checked to
// still be the one the decision was made on (else nothing is replaced: it changed meanwhile), and is kept in
// TrashDir (hard link when possible, so the replacement is atomic).

const (
	// FrameSyncGet requests a file of a sync folder: id (4) + name length (1) + name + sha256 (32) + path.
	FrameSyncGet = 'Q'

	syncIDBit = 1 << 31
	// syncParallel is how many files we get at the same time from a peer for a folder.
	syncParallel = 4
	// syncRetry is how long before trying again to get a path that failed (eg still being written there).
	syncRetry = 3 * time.Second
	// syncStable is how long a file must be unchanged before we serve it (it may still be being written).
	syncStable = 2 * time.Second
	// trashTimeLayout is the suffix (after ~) of the versions in the trash.
	trashTimeLayout = "20060102-150405.000"
)

// syncPull is getting (or creating, for directories, or changing the mode of) a path from a peer.
type syncPull struct {
	f      *syncFolder
	sp     *syncPeer
	peer   Peer
	item   syncdir.Item
	tmp    string // downloaded file (relative to the folder).
	target string // relative to the folder, as on disk.
}

// syncFailure is when and why getting a path last failed, and how many times in a row.
type syncFailure struct {
	at  time.Time
	err string
	n   int
}

// retryAt is when to try again: syncRetry after the first failure, doubling after each next one (up to 64x).
func (sf syncFailure) retryAt() time.Time {
	return sf.at.Add(syncRetry << min(sf.n-1, 6))
}

// recentEntry is a path we just synced, until a scan sees it.
type recentEntry struct {
	e  syncdir.Entry
	at time.Time
}

// startPulls starts getting what the linked peers have newer, up to syncParallel at a time per peer.
func (s *Server) startPulls(f *syncFolder) {
	type request struct {
		l       *link
		conn    net.Conn
		id      uint32
		payload []byte
	}
	var local []*syncPull
	var requests []request
	s.stateMu.Lock()
	now := time.Now()
	todo := make(map[string]bool)
	for _, sp := range f.peers {
		for _, it := range sp.todo {
			todo[it.Path] = true
		}
	}
	for p := range f.failed { // no longer to get (eg in sync now, or a conflict).
		if !todo[p] {
			delete(f.failed, p)
		}
	}
	for peer, sp := range f.peers {
		if !sp.ready || sp.l.conn == nil {
			continue
		}
		for _, it := range slices.Clone(sp.todo) { // a failure replans (sp.todo changes).
			if len(sp.pulls) >= syncParallel {
				break
			}
			if fail, failed := f.failed[it.Path]; f.inflight[it.Path] || (failed && now.Before(fail.retryAt())) {
				continue
			}
			p := &syncPull{f: f, sp: sp, peer: peer, item: it, target: it.Path}
			if it.Local != nil {
				p.target = it.Local.DiskPath()
			}
			f.inflight[it.Path] = true
			if it.Action == syncdir.MetadataOnly || it.Remote.Dir {
				local = append(local, p)
				continue
			}
			id, err := s.preparePullLocked(p, now)
			if err != nil {
				s.syncDoneLocked(p, err)
				continue
			}
			payload := binary.BigEndian.AppendUint32(nil, id)
			payload = append(append(append(payload, byte(len(f.Name))), f.Name...), it.Remote.Hash[:]...) //nolint:gosec // validated.
			requests = append(requests, request{sp.l, sp.l.conn, id, append(payload, it.Path...)})
		}
	}
	s.stateMu.Unlock()
	for _, p := range local {
		err := p.localOp()
		s.stateMu.Lock()
		s.syncDoneLocked(p, err)
		s.stateMu.Unlock()
	}
	for _, r := range requests {
		if err := r.l.send(r.conn, FrameSyncGet, r.payload); err != nil {
			s.stateMu.Lock()
			s.dropIncomingLocked(r.l, r.id, err)
			s.stateMu.Unlock()
		}
	}
}

// preparePullLocked creates the file to download into and registers the transfer, returns its id.
func (s *Server) preparePullLocked(p *syncPull, now time.Time) (uint32, error) {
	r, err := os.OpenRoot(p.f.Path)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	dir := path.Dir(p.target)
	if err = r.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
		return 0, pathErr(err)
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	p.tmp = filepath.FromSlash(path.Join(dir, "."+path.Base(p.target)+"."+hex.EncodeToString(rnd)+".tsync-partial"))
	fh, err := r.OpenFile(p.tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, pathErr(err)
	}
	s.nextSyncID++
	id := syncIDBit | s.nextSyncID&^syncIDBit
	rem := p.item.Remote
	t := &Transfer{
		Peer: p.peer, ID: id, Incoming: true, Name: path.Base(p.item.Path), Source: p.item.Path, Size: rem.Size,
		MTime: rem.MTime, Start: now, State: TransferActive, pull: p,
	}
	p.sp.pulls[id] = t
	p.sp.l.incoming[id] = &recvFile{f: fh, hash: sha256.New(), t: t}
	return id, nil
}

// localOp creates a directory or changes a mode (no transfer needed).
func (p *syncPull) localOp() error {
	r, err := os.OpenRoot(p.f.Path)
	if err != nil {
		return err
	}
	defer r.Close()
	target := filepath.FromSlash(p.target)
	mode := syncdir.KeptMode(p.item.Remote.Mode)
	if p.item.Action == syncdir.MetadataOnly {
		if err = checkUnchanged(r, target, p.item.Local); err != nil {
			return err
		}
	} else if err = r.MkdirAll(target, 0o755); err != nil {
		return pathErr(err)
	}
	return pathErr(r.Chmod(target, mode))
}

// removeTemp removes the downloaded file (failed or aborted).
func (p *syncPull) removeTemp() {
	if r, err := os.OpenRoot(p.f.Path); err == nil {
		_ = r.Remove(p.tmp)
		_ = r.Close()
	}
}

// checkUnchanged checks that the file at target is still the local entry the decision was made on (absent
// if nil): same size and time (if not too recent), else same content.
func checkUnchanged(r *os.Root, target string, local *syncdir.Entry) error {
	fi, err := r.Lstat(target)
	if local == nil {
		if err == nil {
			return errors.New("created here meanwhile")
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return pathErr(err)
	}
	if err != nil {
		return pathErr(err)
	}
	if local.Dir != fi.IsDir() || (!fi.IsDir() && !fi.Mode().IsRegular()) {
		return errors.New("changed here meanwhile")
	}
	if fi.IsDir() || (fi.Size() == local.Size && fi.ModTime().Equal(local.MTime) && time.Since(fi.ModTime()) > syncStable) {
		return nil
	}
	f, err := r.Open(target)
	if err != nil {
		return pathErr(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if !slices.Equal(h.Sum(nil), local.Hash[:]) {
		return errors.New("modified here meanwhile")
	}
	return nil
}

// syncFileEnd handles the end of a downloaded file: checks it, then replaces ours (in a goroutine, not to
// block the read loop) and tells the peer.
func (s *Server) syncFileEnd(l *link, conn net.Conn, id uint32, rf *recvFile, sum []byte) {
	t := rf.t
	s.stateMu.Lock()
	done := t.Done
	delete(l.incoming, id)
	s.stateMu.Unlock()
	var err error
	switch {
	case done != t.Size:
		err = fmt.Errorf("incomplete: %d of %d bytes", done, t.Size)
	case !slices.Equal(sum, rf.hash.Sum(nil)):
		err = errors.New("sha256 mismatch")
	case !slices.Equal(sum, t.pull.item.Remote.Hash[:]):
		err = errors.New("not the expected version")
	}
	if cerr := rf.f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		s.wg.Go(func() {
			s.pullResult(l, conn, t, t.pull.replace(s.SyncKeep))
		})
		return
	}
	s.pullResult(l, conn, t, err)
}

// pullResult tells the peer and records the result of a download.
func (s *Server) pullResult(l *link, conn net.Conn, t *Transfer, err error) {
	if err != nil {
		t.pull.removeTemp()
		_ = l.send(conn, FrameFileRefuse, idPayload(t.ID, err.Error()))
	} else {
		_ = l.send(conn, FrameFileSaved, idPayload(t.ID, ""))
	}
	s.stateMu.Lock()
	s.finishLocked(t, err)
	s.stateMu.Unlock()
}

// replace replaces (or creates) the target with the downloaded file, if ours didn't change meanwhile;
// keeps the replaced version in the trash (keep versions per file, none if 0).
func (p *syncPull) replace(keep int) error {
	r, err := os.OpenRoot(p.f.Path)
	if err != nil {
		return err
	}
	defer r.Close()
	target := filepath.FromSlash(p.target)
	if err = checkUnchanged(r, target, p.item.Local); err != nil {
		return err
	}
	rem := p.item.Remote
	_ = r.Chmod(p.tmp, syncdir.KeptMode(rem.Mode))
	_ = r.Chtimes(p.tmp, rem.MTime, rem.MTime)
	if p.item.Local != nil && keep > 0 {
		if err = toTrash(r, target, p.item.Path, keep); err != nil {
			return fmt.Errorf("can't keep the replaced version: %w", err)
		}
	}
	return pathErr(r.Rename(p.tmp, target))
}

// toTrash keeps the file at target in the trash (TrashDir/<key>~<time>), at most keep versions per file.
func toTrash(r *os.Root, target, key string, keep int) error {
	dir := path.Join(syncdir.TrashDir, path.Dir(key))
	if err := r.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
		return pathErr(err)
	}
	base := path.Base(key)
	version := filepath.FromSlash(path.Join(dir, base+"~"+time.Now().UTC().Format(trashTimeLayout)))
	if err := r.Link(target, version); err != nil { // eg not supported by the file system.
		if err = r.Rename(target, version); err != nil {
			return pathErr(err)
		}
	}
	versions := trashVersions(r, dir, base)
	for len(versions) > keep {
		_ = r.Remove(filepath.FromSlash(path.Join(dir, versions[0])))
		versions = versions[1:]
	}
	return nil
}

// trashVersions returns the names of the versions of a file in the trash directory dir, oldest first.
func trashVersions(r *os.Root, dir, base string) []string {
	entries, err := fs.ReadDir(r.FS(), dir)
	if err != nil {
		return nil
	}
	var res []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && len(name) == len(base)+1+len(trashTimeLayout) && strings.HasPrefix(name, base+"~") {
			res = append(res, name)
		}
	}
	slices.Sort(res)
	return res
}

// pruneTrash removes the oldest versions in the trash of root, while it is over maxSize bytes, except the
// latest version of each file.
func pruneTrash(root string, maxSize int64) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return
	}
	defer r.Close()
	type version struct {
		p    string // slash separated, relative to the root.
		when string
		size int64
	}
	var all []version
	latest := make(map[string]string) // file (without the suffix) -> its latest version.
	var total int64
	_ = fs.WalkDir(r.FS(), syncdir.TrashDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(d.Name()) <= len(trashTimeLayout)+1 {
			return nil //nolint:nilerr // best effort.
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil //nolint:nilerr // best effort.
		}
		file, when := p[:len(p)-len(trashTimeLayout)-1], p[len(p)-len(trashTimeLayout):]
		all = append(all, version{p, when, fi.Size()})
		total += fi.Size()
		latest[file] = max(latest[file], p)
		return nil
	})
	if total <= maxSize {
		return
	}
	slices.SortFunc(all, func(a, b version) int { return strings.Compare(a.when, b.when) })
	for _, v := range all {
		if total <= maxSize {
			break
		}
		if latest[v.p[:len(v.p)-len(trashTimeLayout)-1]] == v.p {
			continue
		}
		if r.Remove(filepath.FromSlash(v.p)) == nil {
			total -= v.size
		}
	}
	log.LogVf("Sync trash of %s pruned to %d bytes", root, total)
}

// syncDoneLocked records the result of getting a path from a peer.
func (s *Server) syncDoneLocked(p *syncPull, err error) {
	f, it := p.f, p.item
	delete(f.inflight, it.Path)
	now := time.Now()
	if err != nil {
		prev, ok := f.failed[it.Path]
		if !ok || prev.err != err.Error() { // not again on each retry.
			log.Warnf("Sync %q: can't get %q from %s (will retry): %v", f.Name, it.Path, p.peer.Name, err)
		}
		f.failed[it.Path] = syncFailure{now, err.Error(), prev.n + 1}
	} else {
		delete(f.failed, it.Path)
		e := *it.Remote
		if it.Local != nil {
			e = *it.Local // keeps its path on disk.
			e.Dir, e.Size, e.MTime, e.Hash = it.Remote.Dir, it.Remote.Size, it.Remote.MTime, it.Remote.Hash
		}
		e.Mode = syncdir.KeptMode(it.Remote.Mode)
		f.recent[it.Path] = recentEntry{e, now}
		p.sp.base.Record(e)
		p.sp.dirty = true
		f.trashDirty = f.trashDirty || it.Local != nil
		switch {
		case it.Action == syncdir.MetadataOnly:
			log.Infof("Sync %q: mode of %q set to %v as on %s", f.Name, it.Path, e.Mode, p.peer.Name)
		case !e.Dir:
			log.Infof("Sync %q: got %q (%d bytes) from %s", f.Name, it.Path, e.Size, p.peer.Name)
		}
	}
	if len(f.inflight) == 0 {
		f.rescan = true // so the peers learn what we got.
	}
	for peer, sp := range f.peers {
		s.replanLocked(f, peer, sp)
	}
	f.kickLocked()
	s.change(0)
}

// pullFinishedLocked is called when a download transfer finished (or failed).
func (s *Server) pullFinishedLocked(t *Transfer, err error) {
	if _, ok := t.pull.sp.pulls[t.ID]; !ok {
		return
	}
	delete(t.pull.sp.pulls, t.ID)
	s.syncDoneLocked(t.pull, err)
}

// syncGetReceived serves a file of a sync folder to the peer, if it is still the version it asked for.
func (s *Server) syncGetReceived(peer Peer, l *link, conn net.Conn, b []byte) {
	if len(b) < 5 || len(b) < 5+int(b[4])+len(syncdir.Hash{}) {
		log.Warnf("Invalid sync get frame from %s", peer.Name)
		return
	}
	id, n := binary.BigEndian.Uint32(b), int(b[4])
	name := string(b[5 : 5+n])
	var hash syncdir.Hash
	copy(hash[:], b[5+n:])
	key := string(b[5+n+len(hash):])
	if reason := s.startSyncSendLocked(peer, l, conn, id, name, hash, key); reason != "" {
		log.LogVf("Not sending %q of sync folder %q to %s: %s", key, name, peer.Name, reason)
		_ = l.send(conn, FrameFileAbort, idPayload(id, reason))
	}
}

// startSyncSendLocked takes the lock and starts sending a file of a sync folder, returns the reason if it can't.
func (s *Server) startSyncSendLocked(peer Peer, l *link, conn net.Conn, id uint32, name string, hash syncdir.Hash,
	key string,
) string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	f := s.syncFolders[name]
	if f == nil || !l.syncFolders[name] {
		return "unknown sync folder"
	}
	e, ok := f.local.Entries[key]
	switch {
	case !ok || e.Dir || e.Hash != hash:
		return "changed since"
	case time.Since(e.MTime) < syncStable:
		return "still being written"
	case l.outgoing[id] != nil:
		return reasonAlreadySending
	}
	t := &Transfer{
		Peer: peer, ID: id, Name: path.Base(key), Source: e.DiskPath(), Path: filepath.Join(f.Path, filepath.FromSlash(e.DiskPath())),
		Size: e.Size, MTime: e.MTime, Start: time.Now(), State: TransferActive, root: f.Path, syncFolder: name,
	}
	l.outgoing[id] = t
	s.wg.Add(1)
	go s.sendFile(peer, l, conn, t)
	return ""
}

// saveBases saves the bases that changed.
func (s *Server) saveBases(f *syncFolder) {
	s.stateMu.Lock()
	saves := f.toSave
	f.toSave = nil
	for _, sp := range f.peers {
		if sp.dirty && sp.baseFile != "" {
			saves = append(saves, savedBase{sp.baseFile, maps.Clone(sp.base)})
		}
		sp.dirty = false
	}
	s.stateMu.Unlock()
	for _, sv := range saves {
		if err := sv.b.Save(sv.file); err != nil {
			log.Errf("Can't save the sync base %s: %v", sv.file, err)
		}
	}
}
