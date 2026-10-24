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

	"fortio.org/log"
)

// Directory send: a directory is offered as one offer (FrameDirOffer). When accepted the
// sender replies with the manifest (relative path, mode, size, mtime of each regular file,
// then of each sub directory) and the receiver then downloads the files one by one (a few in
// parallel) with the regular file frames, using the ids following the directory's one
// (directory id + 1 + file index). The files are saved in a new directory (never an existing
// one: name-1, name-2...) and the sender, receiver and the UI see a single aggregated transfer.
// The permissions (except group/other write) and times of the files and directories are kept,
// once all the files are received for the directories.

// reasonAlreadySending is the abort reason when a file is requested again while being sent.
const reasonAlreadySending = "already being sent"

const (
	FrameDirOffer = 'M' // sender offers a directory: id (4) + size (8) + mtime (8) + file count (4) + path (on the sender).
	// FrameDirManifest is the sender's reply to the request: id (4) + directory mode (4) + entries:
	// mode (4) + size (8) + mtime (8) + path length (2) + path; files first, then directories (ModeDir set).
	FrameDirManifest = 'N'
	// manifestEntrySize is the size of a manifest entry without its path.
	manifestEntrySize = 22

	// maxDirManifest is the maximum size of a manifest, so it fits in a frame.
	maxDirManifest = MaxClipSize - 1024
	// maxDirDepth limits the depth of the directories we create.
	maxDirDepth = 64
	// dirParallel is how many files of a directory are downloaded at the same time.
	dirParallel = 4
)

// dirEntry is a file (or sub directory) in an offered directory.
type dirEntry struct {
	rel   string // slash separated path relative to the offered directory.
	path  string // sender: absolute path.
	mode  fs.FileMode
	size  int64
	mtime time.Time
}

// recvDir is a directory being downloaded (access with stateMu held).
type recvDir struct {
	t       *Transfer
	l       *link
	root    string
	entries []recvEntry
	dirs    []recvEntry // sub directories (dir is the directory itself).
	mode    fs.FileMode // of the directory itself.
	next    int         // next entry to request.
	started bool
}

type recvEntry struct {
	dir   string // destination directory (absolute).
	name  string // sanitized file name.
	rel   string // sanitized path relative to the directory (for display).
	mode  fs.FileMode
	size  int64
	mtime time.Time
}

type dirKey struct {
	peer Peer
	id   uint32
}

// walkDir lists the regular files and the sub directories of dir (not following symlinks), returns how many
// other entries were skipped.
func walkDir(dir string) (files, dirs []dirEntry, skipped int, err error) {
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
		if p == dir {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil || (!fi.Mode().IsRegular() && !fi.IsDir()) {
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
		if strings.Count(rel, "/") >= maxDirDepth { // the receiver would refuse it.
			return fmt.Errorf("directory too deep (over %d levels)", maxDirDepth)
		}
		size += manifestEntrySize + len(rel)
		if size > maxDirManifest || len(rel) > 0xffff {
			return errors.New("too many files (or too long names) in directory")
		}
		e := dirEntry{rel: rel, path: p, mode: fi.Mode() & (fs.ModeDir | fs.ModePerm), mtime: fi.ModTime()}
		if fi.IsDir() {
			dirs = append(dirs, e)
		} else {
			e.size = fi.Size()
			files = append(files, e)
		}
		return nil
	})
	return files, dirs, skipped, err
}

// listDir sets the files (and total size) of a directory offer.
func (o *offer) listDir() error {
	var err error
	// The walk doesn't follow symlinks, including the directory itself (eg o.path is a link to elsewhere).
	if o.root, err = filepath.EvalSymlinks(o.path); err != nil {
		return pathErr(err)
	}
	fi, err := os.Stat(o.root)
	if err != nil {
		return pathErr(err)
	}
	o.mode = fi.Mode().Perm()
	entries, dirs, skipped, err := walkDir(o.root)
	if err != nil {
		return pathErr(err)
	}
	if len(entries) == 0 {
		return errors.New("no files in directory")
	}
	if skipped > 0 {
		log.Warnf("Skipped %d unreadable or special entries (eg symlinks) in %q", skipped, o.path)
	}
	o.dir, o.dirs, o.size = entries, dirs, 0
	for _, e := range entries {
		o.size += e.size
	}
	return nil
}

// dirOfferPayload is the payload of a directory offer: the file one with the file count before the path.
func dirOfferPayload(o *offer) []byte {
	p := offerPayload(o)
	count := binary.BigEndian.AppendUint32(nil, uint32(len(o.dir))) //nolint:gosec // limited by maxDirManifest.
	return slices.Concat(p[:20], count, p[20:])
}

func manifestPayload(o *offer) []byte {
	b := binary.BigEndian.AppendUint32(nil, o.id)
	b = binary.BigEndian.AppendUint32(b, uint32(o.mode))
	for _, e := range slices.Concat(o.dir, o.dirs) {
		b = binary.BigEndian.AppendUint32(b, uint32(e.mode))
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
	handled, reason, manifest := s.startDirSendLocked(peer, l, conn, id)
	switch {
	case reason != "":
		_ = l.send(conn, FrameFileAbort, idPayload(id, reason))
	case manifest != nil:
		if err := l.send(conn, FrameDirManifest, manifest); err != nil {
			log.Errf("Failed to send directory manifest to %s: %v", peer.Name, err)
		}
	}
	return handled
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

// startDirSendLocked takes the lock, returns whether id is a directory's (or one of its files), the
// reason to abort if it can't be sent, and for the directory itself its manifest to send.
func (s *Server) startDirSendLocked(peer Peer, l *link, conn net.Conn, id uint32) (bool, string, []byte) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	o, idx := s.dirOfferForLocked(id)
	if o == nil {
		return false, "", nil
	}
	if idx < 0 { // the directory itself.
		if l.outgoing[id] != nil {
			return true, reasonAlreadySending, nil
		}
		t := &Transfer{
			Peer: peer, ID: id, Name: o.name, Path: o.path, Size: o.size, MTime: o.mtime, Start: time.Now(),
			Dir: true, Files: len(o.dir), State: TransferActive,
		}
		s.addTransferLocked(t)
		l.outgoing[id] = t
		log.Infof("Sending directory %q (%d files, %d bytes) to %s", o.name, t.Files, t.Size, peer.Name)
		return true, "", manifestPayload(o)
	}
	parent := l.outgoing[o.id]
	if parent == nil || parent.State != TransferActive {
		return true, "directory not requested", nil
	}
	if l.outgoing[id] != nil {
		return true, reasonAlreadySending, nil
	}
	e := o.dir[idx]
	t := &Transfer{
		Peer: peer, ID: id, Name: filepath.Base(e.path), Source: e.rel, Path: e.path, Size: e.size, MTime: e.mtime,
		Start: time.Now(), State: TransferActive, parent: parent, root: o.root,
	}
	parent.addKid(t)
	l.outgoing[id] = t
	s.wg.Add(1)
	go s.sendFile(peer, l, conn, t)
	return true, "", nil
}

// failDirSendLocked fails a directory we are sending and stops its files in progress (the
// receiver is told with an abort for each).
func (s *Server) failDirSendLocked(p *Transfer, err error) {
	s.finishLocked(p, err)
	for _, k := range p.kids {
		if k.cancel == "" {
			k.cancel = "directory transfer failed"
		}
	}
}

// forgetDirLocked removes the directory of a file we sent from the link's outgoing ones once it is finished.
func forgetDirLocked(l *link, t *Transfer) {
	if p := t.parent; p != nil && p.State != TransferActive {
		delete(l.outgoing, p.ID)
	}
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
		err = fmt.Errorf("%s: %w", t.Source, err)
		if p.Incoming {
			s.finishLocked(p, err)
		} else {
			s.failDirSendLocked(p, err)
		}
		return
	}
	p.filesDone++
	if p.filesDone >= p.Files {
		if rd := s.rdirs[dirKey{p.Peer, p.ID}]; rd != nil && p.Incoming {
			rd.setAttrs()
		}
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
	if files <= 0 || files > maxDirManifest/manifestEntrySize {
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
// and file name parts. Each part goes through [SafeFileName] (so it can't be empty, "." or "..", or contain
// separators, drive letters...) after backslashes, which are valid in unix file names, are replaced, but
// hidden files and directories (leading dot) are kept as such.
func safeRelPath(rel string) ([]string, error) {
	parts := strings.Split(rel, "/")
	if len(parts) > maxDirDepth {
		return nil, errors.New("path too deep")
	}
	res := make([]string, len(parts))
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, fmt.Errorf("invalid path %q", sanitizeReason([]byte(rel)))
		}
		res[i] = safeFileName(strings.ReplaceAll(p, `\`, "_"), true)
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
	if err == nil {
		err = rd.makeDirs()
	}
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
	if len(b) < 4 {
		return errors.New("truncated")
	}
	rd.mode = fs.FileMode(binary.BigEndian.Uint32(b))
	b = b[4:]
	var total int64
	for len(b) > 0 {
		if len(b) < manifestEntrySize {
			return errors.New("truncated")
		}
		mode := fs.FileMode(binary.BigEndian.Uint32(b[0:4]))
		size := int64(binary.BigEndian.Uint64(b[4:12]))                 //nolint:gosec // checked below.
		mtime := time.Unix(0, int64(binary.BigEndian.Uint64(b[12:20]))) //nolint:gosec // any value is fine.
		n := int(binary.BigEndian.Uint16(b[20:22]))
		isDir := mode.IsDir()
		if size < 0 || len(b) < manifestEntrySize+n || (isDir && size != 0) || (!isDir && len(rd.entries) >= rd.t.Files) {
			return errors.New("invalid entry")
		}
		parts, err := safeRelPath(string(b[manifestEntrySize : manifestEntrySize+n]))
		if err != nil {
			return err
		}
		b = b[manifestEntrySize+n:]
		e := recvEntry{
			dir: filepath.Join(append([]string{rd.root}, parts[:len(parts)-1]...)...), name: parts[len(parts)-1],
			rel: strings.Join(parts, "/"), mode: mode.Perm(), size: size, mtime: mtime,
		}
		if isDir {
			e.dir = filepath.Join(e.dir, e.name)
			rd.dirs = append(rd.dirs, e)
			continue
		}
		rd.entries = append(rd.entries, e)
		total += size
	}
	if len(rd.entries) != rd.t.Files || total != rd.t.Size {
		return fmt.Errorf("manifest has %d files, %d bytes instead of %d, %d", len(rd.entries), total, rd.t.Files, rd.t.Size)
	}
	return nil
}

// makeDirs creates the sub directories (including empty ones) before the files are downloaded.
func (rd *recvDir) makeDirs() error {
	for _, d := range rd.dirs {
		if err := os.MkdirAll(d.dir, 0o755); err != nil {
			return pathErr(err)
		}
	}
	return nil
}

// keptMode is the mode we set on received files and directories (of a directory): the sender's
// permissions, except group and other write (eg Windows shows all files as writable by all).
func keptMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() &^ 0o022
}

// setAttrs sets the mode and times of the directories once all the files are written: deepest first so
// a directory without write or search permission doesn't prevent setting its sub directories'.
func (rd *recvDir) setAttrs() {
	dirs := append(slices.Clone(rd.dirs), recvEntry{dir: rd.root, mode: rd.mode, mtime: rd.t.MTime})
	for _, d := range slices.Backward(dirs) {
		err := os.Chmod(d.dir, keptMode(d.mode))
		if err == nil {
			err = os.Chtimes(d.dir, d.mtime, d.mtime)
		}
		if err != nil {
			log.Warnf("Can't set the mode or time of %q: %v", d.dir, pathErr(err))
		}
	}
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
	l.incoming[id] = &recvFile{f: f, hash: sha256.New(), t: child, dest: e.dir, mode: keptMode(e.mode)}
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
