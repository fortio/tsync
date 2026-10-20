package tsnet

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fortio.org/log"
)

// File transfer over the TCP link: the sender offers a file (name, size, time) to its
// linked peers; a receiver that accepts (user action) requests it, the sender then
// streams data chunks and an end frame with the sha256 of the content. The receiver
// writes to a temporary file in FileDir and only renames it to its (sanitized, never
// overwriting) final name once the size and hash match, then confirms. Either side
// can abort.

const (
	FrameFileOffer  = 'O' // sender offers: id (4) + size (8) + mtime unix nano (8) + path (on the sender).
	FrameFileGet    = 'G' // receiver requests the offered file: id (4).
	FrameFileData   = 'D' // id (4) + data.
	FrameFileEnd    = 'E' // id (4) + sha256 (32).
	FrameFileAbort  = 'X' // sender aborts: id (4) + reason.
	FrameFileRefuse = 'R' // receiver aborts: id (4) + reason.
	FrameFileSaved  = 'A' // receiver confirms the file was received and saved: id (4).

	// FileChunkSize is the size of file data frames.
	FileChunkSize = 256 * 1024
	// MaxFileNameLen is the maximum length in bytes of a received file name.
	MaxFileNameLen = 200
	// maxTransfers is how many offers/finished transfers we keep (for display).
	maxTransfers = 20
	// progressInterval limits how often progress triggers OnChange.
	progressInterval = 100 * time.Millisecond
	// expiredOfferLinger is how long offers that can't be downloaded anymore stay listed.
	expiredOfferLinger = 5 * time.Second
)

type TransferState int

const (
	TransferOffered TransferState = iota // incoming offer, not (yet) accepted.
	TransferActive
	TransferDone
	TransferFailed
)

func (ts TransferState) String() string {
	switch ts {
	case TransferOffered:
		return "Offered"
	case TransferActive:
		return "Active"
	case TransferDone:
		return "Done"
	case TransferFailed:
		return "Failed"
	default:
		return "TransferState(" + strconv.Itoa(int(ts)) + ")"
	}
}

// Transfer is the state of a file offered to us, or being (or that was) sent or received.
type Transfer struct {
	Peer     Peer
	ID       uint32 // the sender's offer id.
	Incoming bool
	Name     string // file name (incoming: as saved once done).
	Path     string // outgoing: source path; incoming: where it was saved once done.
	Source   string // incoming: the path on the sender (for display only).
	Size     int64
	MTime    time.Time // file modification time.
	Done     int64     // bytes transferred so far.
	State    TransferState
	Err      string
	Start    time.Time // when offered (incoming) or requested (outgoing).
	End      time.Time
	cancel   string // outgoing: set (reason) when the receiver aborted.
	sent     bool   // outgoing: all sent, waiting for the receiver's confirmation.
	lastProg time.Time
	expired  bool // offer failed because the peer restarted or is gone: removed after a bit.
}

// offer is a file we offered (sent to our linked peers, and later linked ones).
type offer struct {
	id    uint32
	path  string
	name  string
	size  int64
	mtime time.Time
}

// recvFile is an incoming file being written. The map is accessed with stateMu held,
// the file itself only by the link's read loop once in the map.
type recvFile struct {
	f    *os.File
	hash hash.Hash
	t    *Transfer
}

// Transfers returns a copy of the current and recent offers and transfers, oldest first.
func (s *Server) Transfers() []Transfer {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	res := make([]Transfer, 0, len(s.transfers))
	for _, t := range s.transfers {
		res = append(res, *t)
	}
	return res
}

// addTransferLocked adds a transfer and prunes the oldest finished (or offered) ones.
func (s *Server) addTransferLocked(t *Transfer) {
	s.transfers = append(s.transfers, t)
	for len(s.transfers) > maxTransfers {
		idx := -1
		for i, old := range s.transfers {
			if old.State != TransferActive {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		s.transfers = append(s.transfers[:idx], s.transfers[idx+1:]...)
	}
	s.change(0)
}

func (s *Server) progressLocked(t *Transfer, n int64) {
	t.Done += n
	if now := time.Now(); now.Sub(t.lastProg) >= progressInterval {
		t.lastProg = now
		s.change(0)
	}
}

func (s *Server) finishLocked(t *Transfer, err error) {
	t.End = time.Now()
	if err != nil {
		t.State = TransferFailed
		t.Err = err.Error()
	} else {
		t.State = TransferDone
	}
	s.change(0)
}

// CheckFile checks that path is a regular file we can read (that can be offered).
// The error doesn't include the path (eg "no such file or directory").
func CheckFile(path string) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, pathErr(err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file (directories aren't supported yet)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, pathErr(err)
	}
	_ = f.Close()
	return fi, nil
}

// pathErr returns the underlying error of a *fs.PathError (without the operation and path).
func pathErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

// OfferFile offers the file to all the linked peers, and to peers linked later (our last
// offers are sent to newly linked peers). Returns how many peers it was offered to now.
func (s *Server) OfferFile(path string) (int, error) {
	fi, err := CheckFile(path)
	if err != nil {
		return 0, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	s.stateMu.Lock()
	s.nextFileID++
	o := &offer{id: s.nextFileID, path: abs, name: filepath.Base(abs), size: fi.Size(), mtime: fi.ModTime()}
	s.offers[o.id] = o
	delete(s.offers, o.id-maxTransfers) // only keep the last ones.
	type target struct {
		peer Peer
		l    *link
		conn net.Conn
	}
	var targets []target
	for peer, l := range s.links {
		if l.conn != nil {
			targets = append(targets, target{peer, l, l.conn})
		}
	}
	s.stateMu.Unlock()
	n := 0
	var errs []error
	for _, t := range targets {
		if err := t.l.send(t.conn, FrameFileOffer, offerPayload(o)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.peer.Name, err))
			continue
		}
		n++
	}
	log.Infof("Offered file %q (%d bytes) to %d peers", o.name, o.size, n)
	return n, errors.Join(errs...)
}

func offerPayload(o *offer) []byte {
	b := make([]byte, 20, 20+len(o.path))
	binary.BigEndian.PutUint32(b, o.id)
	binary.BigEndian.PutUint64(b[4:], uint64(o.size)) //nolint:gosec // size is positive.
	binary.BigEndian.PutUint64(b[12:], uint64(o.mtime.UnixNano()))
	return append(b, o.path...)
}

// sendOffers sends our (recent) file offers to a newly linked peer, oldest first.
func (s *Server) sendOffers(peer Peer, l *link, conn net.Conn) {
	s.stateMu.Lock()
	offers := make([]*offer, 0, len(s.offers))
	for _, o := range s.offers {
		offers = append(offers, o)
	}
	s.stateMu.Unlock()
	slices.SortFunc(offers, func(a, b *offer) int { return cmp.Compare(a.id, b.id) })
	for _, o := range offers {
		if err := l.send(conn, FrameFileOffer, offerPayload(o)); err != nil {
			log.Errf("Failed to send file offers to %s: %v", peer.Name, err)
			return
		}
	}
}

// AcceptFile downloads a file offered by peer (see [Server.Transfers]).
func (s *Server) AcceptFile(peer Peer, id uint32) error {
	l, err := s.prepareDownload(peer, id)
	if err != nil {
		return err
	}
	// Sent without holding stateMu (the link writer may be busy), the download is
	// already registered so incoming data finds it.
	if err = l.send(l.conn, FrameFileGet, idPayload(id, "")); err != nil {
		s.stateMu.Lock()
		s.dropIncomingLocked(l, id, err)
		s.stateMu.Unlock()
		return err
	}
	return nil
}

func (s *Server) prepareDownload(peer Peer, id uint32) (*link, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	var t *Transfer
	for _, tr := range s.transfers {
		if tr.Incoming && tr.Peer == peer && tr.ID == id && tr.State == TransferOffered {
			t = tr
		}
	}
	if t == nil {
		return nil, fmt.Errorf("no pending file offer %d from %s", id, peer.Name)
	}
	l := s.links[peer]
	if l == nil || l.conn == nil {
		return nil, fmt.Errorf("not linked to %s (anymore)", peer.Name)
	}
	if l.incoming[id] != nil {
		return nil, fmt.Errorf("file %q from %s already being downloaded", t.Name, peer.Name)
	}
	f, err := os.CreateTemp(s.fileDir(), "."+t.Name+".*.tsync-partial")
	if err != nil {
		s.finishLocked(t, err)
		return nil, err
	}
	l.incoming[id] = &recvFile{f: f, hash: sha256.New(), t: t}
	t.State = TransferActive
	t.Start = time.Now()
	s.change(0)
	log.Infof("Downloading file %q (%d bytes) from %s", t.Name, t.Size, peer.Name)
	return l, nil
}

// startSend streams an offered file to the peer that requested it.
func (s *Server) startSend(peer Peer, l *link, conn net.Conn, id uint32) {
	if reason := s.startSendLocked(peer, l, conn, id); reason != "" {
		_ = l.send(conn, FrameFileAbort, idPayload(id, reason))
	}
}

// startSendLocked takes the lock, returns the reason to abort if it can't be sent.
func (s *Server) startSendLocked(peer Peer, l *link, conn net.Conn, id uint32) string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	o := s.offers[id]
	if o == nil {
		return "no longer offered"
	}
	if l.outgoing[id] != nil {
		return "already being sent"
	}
	t := &Transfer{Peer: peer, ID: id, Name: o.name, Path: o.path, Size: o.size, MTime: o.mtime, Start: time.Now()}
	t.State = TransferActive
	s.addTransferLocked(t)
	l.outgoing[id] = t
	s.wg.Add(1)
	go s.sendFile(peer, l, conn, t)
	return ""
}

// expireOffersLocked fails the pending offers from peer (it restarted or is gone so they can't be downloaded anymore).
func (s *Server) expireOffersLocked(peer Peer, why string) {
	for _, t := range s.transfers {
		if t.Incoming && t.Peer == peer && t.State == TransferOffered {
			s.finishLocked(t, fmt.Errorf("no longer available (%s)", why))
			t.expired = true
		}
	}
}

// pruneExpiredOffersLocked removes the expired offers once they've been shown for a bit.
func (s *Server) pruneExpiredOffersLocked(now time.Time) {
	n := len(s.transfers)
	s.transfers = slices.DeleteFunc(s.transfers, func(t *Transfer) bool {
		return t.expired && now.Sub(t.End) > expiredOfferLinger
	})
	if len(s.transfers) != n {
		s.change(0)
	}
}

func (s *Server) sendFile(peer Peer, l *link, conn net.Conn, t *Transfer) {
	defer s.wg.Done()
	log.Infof("Sending file %q (%d bytes) to %s", t.Name, t.Size, peer.Name)
	err := s.streamFile(l, conn, t)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err == nil && t.cancel == "" {
		t.sent = true // now waiting for the receiver's confirmation (or refusal).
		return
	}
	if err == nil {
		err = fmt.Errorf("aborted by peer: %s", t.cancel)
	}
	if _, ok := l.outgoing[t.ID]; !ok {
		return // already finished (eg link closed).
	}
	delete(l.outgoing, t.ID)
	log.Errf("Sending file %q to %s failed: %v", t.Name, peer.Name, err)
	s.finishLocked(t, err)
}

func (s *Server) streamFile(l *link, conn net.Conn, t *Transfer) error {
	abort := func(reason error) error {
		_ = l.send(conn, FrameFileAbort, idPayload(t.ID, reason.Error()))
		return reason
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return abort(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return abort(err)
	}
	if fi.Size() != t.Size || !fi.ModTime().Equal(t.MTime) {
		return abort(errors.New("file changed since it was offered"))
	}
	h := sha256.New()
	buf := make([]byte, 4+FileChunkSize)
	binary.BigEndian.PutUint32(buf, t.ID)
	var total int64
	for {
		s.stateMu.Lock()
		cancel := t.cancel
		s.stateMu.Unlock()
		if cancel != "" {
			return nil // sendFile reports it.
		}
		n, rerr := f.Read(buf[4:])
		if n > 0 {
			total += int64(n)
			if total > t.Size {
				return abort(errors.New("file grew while sending"))
			}
			h.Write(buf[4 : 4+n])
			if err = l.send(conn, FrameFileData, buf[:4+n]); err != nil {
				return err
			}
			s.stateMu.Lock()
			s.progressLocked(t, int64(n))
			s.stateMu.Unlock()
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return abort(rerr)
		}
	}
	if total != t.Size {
		return abort(fmt.Errorf("file changed size while sending (%d vs %d)", total, t.Size))
	}
	return l.send(conn, FrameFileEnd, append(binary.BigEndian.AppendUint32(nil, t.ID), h.Sum(nil)...))
}

func idPayload(id uint32, reason string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, id), reason...)
}

// outgoingResult handles the receiver's confirmation (err nil) or abort of an outgoing transfer.
func (s *Server) outgoingResult(peer Peer, l *link, id uint32, err error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	t := l.outgoing[id]
	if t == nil {
		return
	}
	if !t.sent {
		if err != nil {
			t.cancel = err.Error() // the sender goroutine will stop and finish it.
		}
		return
	}
	delete(l.outgoing, id)
	if err == nil {
		log.Infof("Sent file %q (%d bytes) to %s", t.Name, t.Size, peer.Name)
	} else {
		err = fmt.Errorf("aborted by peer: %w", err)
		log.Errf("Sending file %q to %s failed: %v", t.Name, peer.Name, err)
	}
	s.finishLocked(t, err)
}

// handleFileFrame processes the file related frames (called from the link's read loop).
// Returns false if the frame type isn't a file one.
func (s *Server) handleFileFrame(peer Peer, l *link, conn net.Conn, plain []byte) bool {
	ftype := plain[0]
	switch ftype {
	case FrameFileOffer, FrameFileGet, FrameFileData, FrameFileEnd, FrameFileAbort, FrameFileRefuse, FrameFileSaved:
	default:
		return false
	}
	if len(plain) < 5 {
		log.Warnf("Invalid file frame %q from %s (%d bytes)", ftype, peer.Name, len(plain))
		return true
	}
	id := binary.BigEndian.Uint32(plain[1:5])
	payload := plain[5:]
	switch ftype {
	case FrameFileOffer:
		s.fileOffered(peer, id, payload)
		return true
	case FrameFileGet:
		s.startSend(peer, l, conn, id)
		return true
	case FrameFileRefuse:
		s.outgoingResult(peer, l, id, errors.New(sanitizeReason(payload)))
		return true
	case FrameFileSaved:
		s.outgoingResult(peer, l, id, nil)
		return true
	}
	s.stateMu.Lock()
	rf := l.incoming[id]
	s.stateMu.Unlock()
	if rf == nil {
		log.LogVf("File frame %q for unknown/aborted transfer %d from %s", ftype, id, peer.Name)
		return true
	}
	var err error
	switch ftype {
	case FrameFileData:
		err = s.fileData(rf, payload)
	case FrameFileEnd:
		err = s.fileEnd(rf, payload)
		if err == nil {
			s.stateMu.Lock()
			delete(l.incoming, id)
			s.stateMu.Unlock()
			_ = l.send(conn, FrameFileSaved, idPayload(id, ""))
			return true
		}
	case FrameFileAbort:
		err = fmt.Errorf("aborted by sender: %s", sanitizeReason(payload))
	}
	if err == nil {
		return true
	}
	if ftype != FrameFileAbort {
		_ = l.send(conn, FrameFileRefuse, idPayload(id, err.Error()))
	}
	s.stateMu.Lock()
	s.dropIncomingLocked(l, id, err)
	s.stateMu.Unlock()
	return true
}

// sanitizeReason makes a (peer provided) error message safe to log/display: single
// line, no control characters, limited length.
func sanitizeReason(b []byte) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(string(b), "?"))
	if len(s) > 200 {
		s = s[:200]
		for s != "" && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func (s *Server) fileOffered(peer Peer, id uint32, payload []byte) {
	if len(payload) < 16 {
		log.Warnf("Invalid file offer frame from %s", peer.Name)
		return
	}
	size := int64(binary.BigEndian.Uint64(payload[0:8]))                 //nolint:gosec // checked below.
	mtime := time.Unix(0, int64(binary.BigEndian.Uint64(payload[8:16]))) //nolint:gosec // any value is fine.
	source := string(payload[16:])
	name := SafeFileName(source) // last path component only (sanitized).
	source = sanitizeReason(payload[16:])
	if size < 0 {
		log.Warnf("Invalid file offer size %d from %s", size, peer.Name)
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for _, t := range s.transfers {
		if t.Incoming && t.Peer == peer && t.ID == id {
			if t.expired { // failed because the peer looked restarted/gone but it is back with the offer.
				t.State, t.Err, t.End, t.expired = TransferOffered, "", time.Time{}, false
				s.change(0)
				return
			}
			return // already known (eg resent on reconnect).
		}
	}
	log.Warnf("%s offers file %q (%d bytes): click or press d to download", peer.Name, source, size)
	s.addTransferLocked(&Transfer{
		Peer: peer, ID: id, Incoming: true, Name: name, Source: source, Size: size, MTime: mtime, Start: time.Now(),
	})
}

func (s *Server) fileData(rf *recvFile, data []byte) error {
	s.stateMu.Lock()
	done := rf.t.Done
	s.stateMu.Unlock()
	if done+int64(len(data)) > rf.t.Size {
		return errors.New("more data than announced")
	}
	if _, err := rf.f.Write(data); err != nil {
		return err
	}
	rf.hash.Write(data)
	s.stateMu.Lock()
	s.progressLocked(rf.t, int64(len(data)))
	s.stateMu.Unlock()
	return nil
}

func (s *Server) fileEnd(rf *recvFile, sum []byte) error {
	t := rf.t
	s.stateMu.Lock()
	done := t.Done
	s.stateMu.Unlock()
	if done != t.Size {
		return fmt.Errorf("incomplete: %d of %d bytes", done, t.Size)
	}
	if !bytes.Equal(sum, rf.hash.Sum(nil)) {
		return errors.New("sha256 mismatch")
	}
	tmp := rf.f.Name()
	if err := rf.f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o644) // regular readable file, never executable.
	_ = os.Chtimes(tmp, t.MTime, t.MTime)
	final, err := reserveName(s.fileDir(), t.Name)
	if err == nil {
		err = os.Rename(tmp, final) // replaces our (empty) reservation.
	}
	if err != nil {
		if final != "" {
			_ = os.Remove(final)
		}
		return err // the caller removes tmp.
	}
	log.Infof("Received file %q (%d bytes) from %s: saved as %s", t.Name, t.Size, t.Peer.Name, final)
	s.stateMu.Lock()
	t.Path = final
	t.Name = filepath.Base(final)
	s.finishLocked(t, nil)
	s.stateMu.Unlock()
	return nil
}

// dropIncomingLocked aborts an incoming transfer: removes the partial file.
func (s *Server) dropIncomingLocked(l *link, id uint32, err error) {
	rf := l.incoming[id]
	if rf == nil {
		return
	}
	delete(l.incoming, id)
	_ = rf.f.Close()
	_ = os.Remove(rf.f.Name())
	log.Warnf("Receiving file %q from %s failed: %v", rf.t.Name, rf.t.Peer.Name, err)
	rf.t.Path = ""
	s.finishLocked(rf.t, err)
}

// linkClosed fails the link's incoming and outgoing transfers.
func (s *Server) linkClosed(l *link) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for id := range l.incoming {
		s.dropIncomingLocked(l, id, errors.New("link closed"))
	}
	for id, t := range l.outgoing {
		delete(l.outgoing, id)
		s.finishLocked(t, errors.New("link closed"))
	}
}

func (s *Server) fileDir() string {
	if s.FileDir == "" {
		return "."
	}
	return s.FileDir
}

// reserveName creates (empty, exclusively) and returns the first available
// "name.ext", "name-1.ext", "name-2.ext"... in dir.
func reserveName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" { // eg ".ext" (can't happen after SafeFileName but just in case).
		base, ext = name, ""
	}
	for i := range 1000 {
		candidate := name
		if i > 0 {
			candidate = base + "-" + strconv.Itoa(i) + ext
		}
		p := filepath.Join(dir, candidate)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no available name for %q in %s", name, dir)
}

// SafeFileName returns a file name safe to create in a directory on any OS from a
// (peer provided) name: last path component only, no control or Windows reserved
// characters, not hidden (no leading dot), not a Windows device name, length limited.
// Letters of any script, accents, emojis... are kept, but look-alike spaces (eg the
// U+202F macOS puts before AM/PM in screenshot names) become regular spaces and invisible
// format characters (bidi overrides that can disguise the extension, zero width spaces...)
// are removed.
func SafeFileName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) || strings.ContainsRune(`<>:"|?*`, r):
			return '_'
		case r != ' ' && r != '\u3000' && unicode.Is(unicode.Zs, r): // keep the CJK ideographic space.
			return ' '
		case r == '\u200c' || r == '\u200d': // zero width (non) joiners: needed by emojis and some scripts.
			return r
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ") // Windows ignores trailing dots/spaces.
	if name == "" {
		return "file"
	}
	if name[0] == '.' {
		name = "_" + name
	}
	if isWindowsReserved(name) {
		name = "_" + name
	}
	if len(name) > MaxFileNameLen {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		cut := MaxFileNameLen - len(ext)
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = name[:cut] + ext
	}
	return name
}

func isWindowsReserved(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '0' && base[3] <= '9'
}
