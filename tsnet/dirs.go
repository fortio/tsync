package tsnet

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fortio.org/log"
	"fortio.org/progressbar"
)

// Directory send: a directory is offered as one offer (FrameDirOffer). When accepted the
// sender replies with the manifest (relative path, size, mtime of each regular file) and
// the receiver then downloads the files one by one (a few in parallel) with the regular
// file frames, using the ids following the directory's one (directory id + 1 + index).
// The files are saved in a new directory (never an existing one: name-1, name-2...) and
// the sender, receiver and the UI see a single aggregated transfer.

const errAlreadySending = "already being sent"

const (
	FrameDirOffer    = 'M' // sender offers a directory: id (4) + size (8) + mtime (8) + file count (4) + path (on the sender).
	FrameDirManifest = 'N' // sender's reply to the request: id (4) + entries (size (8) + mtime (8) + path length (2) + path).

	// maxDirManifest is the maximum size of a manifest, so it fits in a frame.
	maxDirManifest = MaxClipSize - 1024
	// maxDirDepth limits the depth of the directories we create.
	maxDirDepth = 64
	// dirParallel is how many files of a directory are downloaded at the same time.
	dirParallel = 4
)

// dirEntry is a file in an offered directory.
type dirEntry struct {
	rel   string // slash separated path relative to the offered directory.
	path  string // sender: absolute path.
	size  int64
	mtime time.Time
}

// recvDir is a directory being downloaded (access with stateMu held).
type recvDir struct {
	t       *Transfer
	l       *link
	root    string
	entries []recvEntry
	next    int // next entry to request.
	started bool
}

type recvEntry struct {
	dir   string // destination directory (absolute).
	name  string // sanitized file name.
	rel   string // sanitized path relative to the directory (for display).
	size  int64
	mtime time.Time
}

type dirKey struct {
	peer Peer
	id   uint32
}

// walkDir lists the regular files of dir (not following symlinks), returns how many other entries were skipped.
func walkDir(dir string) (entries []dirEntry, skipped int, err error) {
	size := 0
	skip := func() error { //nolint:unparam // always nil: skipped entries aren't errors.
		skipped++
		return nil
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if p == dir {
				return werr
			}
			return skip()
		}
		if d.IsDir() {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil || !fi.Mode().IsRegular() {
			return skip()
		}
		if _, cerr := CheckFile(p); cerr != nil { // unreadable.
			return skip()
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return skip()
		}
		rel = filepath.ToSlash(rel)
		size += 18 + len(rel)
		if size > maxDirManifest || len(rel) > 0xffff {
			return errors.New("too many files (or too long names) in directory")
		}
		entries = append(entries, dirEntry{rel: rel, path: p, size: fi.Size(), mtime: fi.ModTime()})
		return nil
	})
	return entries, skipped, err
}

// dirOfferPayload is the payload of a directory offer: the file one with the file count before the path.
func dirOfferPayload(o *offer) []byte {
	p := offerPayload(o)
	count := binary.BigEndian.AppendUint32(nil, uint32(len(o.dir))) //nolint:gosec // limited by maxDirManifest.
	return slices.Concat(p[:20], count, p[20:])
}

func manifestPayload(o *offer) []byte {
	b := binary.BigEndian.AppendUint32(nil, o.id)
	for _, e := range o.dir {
		b = binary.BigEndian.AppendUint64(b, uint64(e.size)) //nolint:gosec // positive.
		b = binary.BigEndian.AppendUint64(b, uint64(e.mtime.UnixNano()))
		b = binary.BigEndian.AppendUint16(b, uint16(len(e.rel))) //nolint:gosec // checked in walkDir.
		b = append(b, e.rel...)
	}
	return b
}

// startDirSend handles a request for a directory offer or one of its files (sender side).
// Returns false if id isn't one of a directory.
func (s *Server) startDirSend(peer Peer, l *link, conn net.Conn, id uint32) bool {
	reason, manifest := s.startDirSendLocked(peer, l, conn, id)
	switch {
	case reason != "":
		_ = l.send(conn, FrameFileAbort, idPayload(id, reason))
	case manifest != nil:
		if err := l.send(conn, FrameDirManifest, manifest); err != nil {
			log.Errf("Failed to send directory manifest to %s: %v", peer.Name, err)
		}
	}
	return reason != "" || manifest != nil || s.isDirChild(id)
}

func (s *Server) isDirChild(id uint32) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	o, _ := s.dirOfferForLocked(id)
	return o != nil
}

// dirOfferForLocked returns the directory offer containing id (its own id, or one of its files) and the file index (-1 for itself).
func (s *Server) dirOfferForLocked(id uint32) (*offer, int) {
	for _, o := range s.offers {
		if o.dir != nil && id >= o.id && id <= o.id+uint32(len(o.dir)) { //nolint:gosec // limited by maxDirManifest.
			return o, int(id-o.id) - 1
		}
	}
	return nil, 0
}

func (s *Server) startDirSendLocked(peer Peer, l *link, conn net.Conn, id uint32) (string, []byte) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	o, idx := s.dirOfferForLocked(id)
	if o == nil {
		return "", nil
	}
	if idx < 0 { // the directory itself.
		if l.outgoing[id] != nil {
			return errAlreadySending, nil
		}
		t := &Transfer{
			Peer: peer, ID: id, Name: o.name, Path: o.path, Size: o.size, MTime: o.mtime, Start: time.Now(),
			Dir: true, Files: len(o.dir), State: TransferActive,
		}
		s.addTransferLocked(t)
		l.outgoing[id] = t
		log.Infof("Sending directory %q (%d files, %d bytes) to %s", o.name, t.Files, t.Size, peer.Name)
		return "", manifestPayload(o)
	}
	parent := l.outgoing[o.id]
	if parent == nil || parent.State != TransferActive {
		return "directory not requested", nil
	}
	if l.outgoing[id] != nil {
		return errAlreadySending, nil
	}
	e := o.dir[idx]
	t := &Transfer{
		Peer: peer, ID: id, Name: filepath.Base(e.path), Source: e.rel, Path: e.path, Size: e.size, MTime: e.mtime,
		Start: time.Now(), State: TransferActive, parent: parent,
	}
	parent.addKid(t)
	l.outgoing[id] = t
	s.wg.Add(1)
	go s.sendFile(peer, l, conn, t)
	return "", nil
}

// childFinishedLocked updates the directory transfer when one of its files is done (or failed).
func (s *Server) childFinishedLocked(t *Transfer, err error) {
	p := t.parent
	delete(p.kids, t.ID)
	t.End = time.Now()
	t.State = TransferDone
	switch {
	case err != nil:
		t.State = TransferFailed
	case t.Incoming:
		log.Infof("Received %q (%d bytes) of directory %q from %s%s", t.Source, t.Size, p.Name, p.Peer.Name, rateStr(t))
	default:
		log.Infof("Sent %q (%d bytes) of directory %q to %s%s", t.Source, t.Size, p.Name, p.Peer.Name, rateStr(t))
	}
	if p.State != TransferActive {
		return
	}
	if err != nil {
		s.finishLocked(p, fmt.Errorf("%s: %w", t.Name, err))
		return
	}
	p.filesDone++
	if p.filesDone >= p.Files {
		log.Infof("Directory %q (%d files, %d bytes) done with %s%s", p.Name, p.Files, p.Size, p.Peer.Name, rateStr(p))
		s.finishLocked(p, nil)
	}
}

// dirOffered handles a directory offer (receiver side).
func (s *Server) dirOffered(peer Peer, id uint32, payload []byte) {
	if len(payload) < 20 {
		log.Warnf("Invalid directory offer frame from %s", peer.Name)
		return
	}
	files := int(binary.BigEndian.Uint32(payload[16:20]))
	if files <= 0 || files > maxDirManifest/18 {
		log.Warnf("Invalid directory offer (%d files) from %s", files, peer.Name)
		return
	}
	rest := slices.Concat(payload[:16], payload[20:])
	s.fileOffered(peer, id, rest, files)
}

// reserveDir creates (exclusively) and returns the first available "name", "name-1", "name-2"... directory in dir.
func reserveDir(dir, name string) (string, error) {
	for i := range 1000 {
		candidate := name
		if i > 0 {
			candidate = name + "-" + strconv.Itoa(i)
		}
		p := filepath.Join(dir, candidate)
		err := os.Mkdir(p, 0o755)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no available name for directory %q in %s", name, dir)
}

// prepareDirLocked starts the download of a directory offer: creates the destination.
func (s *Server) prepareDirLocked(l *link, t *Transfer) error {
	root, err := reserveDir(s.fileDir(), t.Name)
	if err != nil {
		s.finishLocked(t, err)
		return err
	}
	t.Path, t.Name, t.State, t.Start = root, filepath.Base(root), TransferActive, time.Now()
	s.rdirs[dirKey{t.Peer, t.ID}] = &recvDir{t: t, l: l, root: root}
	s.change(0)
	log.Infof("Downloading directory %q (%d files, %d bytes) from %s into %s", t.Name, t.Files, t.Size, t.Peer.Name, root)
	return nil
}

// safeRelPath validates a (peer provided) slash separated relative path and returns the sanitized directory
// and file name parts.
func safeRelPath(rel string) ([]string, error) {
	parts := strings.Split(rel, "/")
	if len(parts) > maxDirDepth {
		return nil, errors.New("path too deep")
	}
	res := make([]string, len(parts))
	for i, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\`+"\x00") || strings.ContainsFunc(p, unicode.IsControl) {
			return nil, fmt.Errorf("invalid path %q", sanitizeReason([]byte(rel)))
		}
		if strings.ContainsRune(p, ':') && i == 0 && len(p) == 2 { // C:
			return nil, fmt.Errorf("invalid path %q", sanitizeReason([]byte(rel)))
		}
		res[i] = SafeFileName(p)
	}
	return res, nil
}

// dirManifest handles the manifest of a directory we asked for: validates it and starts downloading files.
func (s *Server) dirManifest(peer Peer, l *link, conn net.Conn, id uint32, payload []byte) {
	s.stateMu.Lock()
	rd := s.rdirs[dirKey{peer, id}]
	if rd == nil || rd.l != l || rd.started {
		s.stateMu.Unlock()
		log.Warnf("Unexpected directory manifest from %s", peer.Name)
		return
	}
	rd.started = true
	err := s.parseManifestLocked(rd, payload)
	if err != nil {
		log.Errf("Invalid directory manifest from %s: %v", peer.Name, err)
		s.failDirLocked(rd, err)
		s.stateMu.Unlock()
		_ = l.send(conn, FrameFileRefuse, idPayload(id, err.Error()))
		return
	}
	s.stateMu.Unlock()
	s.requestMoreFiles(peer, l, conn, rd.t)
}

func (s *Server) parseManifestLocked(rd *recvDir, b []byte) error {
	var total int64
	for len(b) > 0 {
		if len(b) < 18 {
			return errors.New("truncated")
		}
		size := int64(binary.BigEndian.Uint64(b[0:8]))                 //nolint:gosec // checked below.
		mtime := time.Unix(0, int64(binary.BigEndian.Uint64(b[8:16]))) //nolint:gosec // any value is fine.
		n := int(binary.BigEndian.Uint16(b[16:18]))
		if size < 0 || len(b) < 18+n || len(rd.entries) >= rd.t.Files {
			return errors.New("invalid entry")
		}
		parts, err := safeRelPath(string(b[18 : 18+n]))
		if err != nil {
			return err
		}
		b = b[18+n:]
		dir := filepath.Join(append([]string{rd.root}, parts[:len(parts)-1]...)...)
		rd.entries = append(rd.entries, recvEntry{
			dir: dir, name: parts[len(parts)-1], rel: strings.Join(parts, "/"), size: size, mtime: mtime,
		})
		total += size
	}
	if len(rd.entries) != rd.t.Files || total != rd.t.Size {
		return fmt.Errorf("manifest has %d files, %d bytes instead of %d, %d", len(rd.entries), total, rd.t.Files, rd.t.Size)
	}
	return nil
}

// requestMoreFiles requests the next files of the directory, keeping dirParallel of them in flight.
func (s *Server) requestMoreFiles(peer Peer, l *link, conn net.Conn, parent *Transfer) {
	for {
		id, err := s.nextDirFile(l, parent)
		if err != nil {
			s.stateMu.Lock()
			if rd := s.rdirs[dirKey{peer, parent.ID}]; rd != nil {
				s.failDirLocked(rd, err)
			}
			s.stateMu.Unlock()
			_ = l.send(conn, FrameFileRefuse, idPayload(parent.ID, err.Error()))
			return
		}
		if id == 0 {
			return
		}
		if err = l.send(conn, FrameFileGet, idPayload(id, "")); err != nil {
			s.stateMu.Lock()
			s.dropIncomingLocked(l, id, err)
			s.stateMu.Unlock()
			return
		}
	}
}

// nextDirFile registers the next file to download (returns its id, or 0 if none now).
func (s *Server) nextDirFile(l *link, parent *Transfer) (uint32, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	rd := s.rdirs[dirKey{parent.Peer, parent.ID}]
	if rd == nil || parent.State != TransferActive || rd.next >= len(rd.entries) {
		return 0, nil
	}
	inflight := 0
	for id := range l.incoming {
		if id > parent.ID && id <= parent.ID+uint32(parent.Files) { //nolint:gosec // limited by maxDirManifest.
			inflight++
		}
	}
	if inflight >= dirParallel {
		return 0, nil
	}
	e := rd.entries[rd.next]
	id := parent.ID + 1 + uint32(rd.next) //nolint:gosec // limited by maxDirManifest.
	rd.next++
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		return 0, pathErr(err)
	}
	f, err := os.CreateTemp(e.dir, "."+e.name+".*.tsync-partial")
	if err != nil {
		return 0, pathErr(err)
	}
	child := &Transfer{
		Peer: parent.Peer, ID: id, Incoming: true, Name: e.name, Source: e.rel, Size: e.size, MTime: e.mtime,
		Start: time.Now(), State: TransferActive, parent: parent,
	}
	parent.addKid(child)
	l.incoming[id] = &recvFile{f: f, hash: sha256.New(), t: child, dest: e.dir}
	return id, nil
}

// failDirLocked fails a directory download.
func (s *Server) failDirLocked(rd *recvDir, err error) {
	if rd.t.State != TransferActive {
		return
	}
	log.Errf("Receiving directory %q from %s failed: %v (%d of %d files received)",
		rd.t.Name, rd.t.Peer.Name, err, rd.t.filesDone, rd.t.Files)
	s.finishLocked(rd.t, fmt.Errorf("%w (%d of %d files received)", err, rd.t.filesDone, rd.t.Files))
}

// dirAborted handles an abort from the sender for a directory we asked for. Returns true if it was one.
func (s *Server) dirAborted(peer Peer, l *link, id uint32, reason string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	rd := s.rdirs[dirKey{peer, id}]
	if rd == nil || rd.l != l {
		return false
	}
	s.failDirLocked(rd, fmt.Errorf("aborted by sender: %s", reason))
	return true
}

// failLinkDirsLocked fails the directories being downloaded through the closed link.
func (s *Server) failLinkDirsLocked(l *link, err error) {
	for _, rd := range s.rdirs {
		if rd.l == l {
			s.failDirLocked(rd, err)
		}
	}
}

// addKid registers a file of a directory transfer as in progress.
func (t *Transfer) addKid(kid *Transfer) {
	if t.kids == nil {
		t.kids = make(map[uint32]*Transfer)
	}
	t.kids[kid.ID] = kid
}

// setCurrent sets the CurFile fields of a directory copy: the first file (in manifest order) in progress.
func (t *Transfer) setCurrent() {
	var cur *Transfer
	for _, k := range t.kids {
		if cur == nil || k.ID < cur.ID {
			cur = k
		}
	}
	if cur != nil {
		t.CurFile, t.CurDone, t.CurSize = cur.Source, cur.Done, cur.Size
	}
}

// rateStr is the average transfer rate of t since it started, with the size, time and speed (as in the UI) for the logs.
func rateStr(t *Transfer) string {
	d := time.Since(t.Start)
	if d <= 0 || t.Size == 0 {
		return ""
	}
	return ", " + progressbar.RateETA(t.Size, t.Size, d)
}
