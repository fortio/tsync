package tsnet

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/tcrypto"
)

// Encrypted TCP link with each Connected peer: once the handshake is done, one side
// (see weDialLocked) dials the other's TCP port (exchanged in the handshake). Frames are
// a 4 byte big endian length followed by the session sealed (AES-GCM) plaintext, which
// is a 1 byte frame type followed by the payload.

const (
	// MaxClipSize is the maximum clipboard payload we send or accept.
	MaxClipSize = 1 << 20
	// maxFrameSize leaves room for the frame type and the AEAD tag.
	maxFrameSize = MaxClipSize + 64

	FrameHello = 'H' // first frame from the dialer, authenticates the connection.
	FrameClip  = 'T' // clipboard: 8 bytes (big endian) unix nano time it was copied, then the text.
	// FrameClipNoTime is the clipboard text without time, sent by older versions.
	FrameClipNoTime = 'C'

	linkDialTimeout  = 3 * time.Second
	linkHelloTimeout = 5 * time.Second // also how long we wait for our side of the handshake to complete.
	linkWriteTimeout = 5 * time.Second
	helloRetryDelay  = 100 * time.Millisecond
)

func writeFrame(w io.Writer, sess *tcrypto.Session, frameType byte, payload []byte) error {
	plain := make([]byte, 0, 1+len(payload))
	plain = append(plain, frameType)
	plain = append(plain, payload...)
	sealed := sess.Seal(plain)
	buf := make([]byte, 4, 4+len(sealed))
	binary.BigEndian.PutUint32(buf, uint32(len(sealed))) //nolint:gosec // bounded by MaxClipSize checks.
	buf = append(buf, sealed...)
	_, err := w.Write(buf)
	return err
}

// readFrame returns the next (still encrypted) frame.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return nil, fmt.Errorf("frame too large: %d > %d", n, maxFrameSize)
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// weDialLocked returns true if we are the side that dials the TCP link to peer.
// Deterministic on both sides and independent of who initiated the handshake.
// (Run ids and not ports, which can differ through NAT.) If that dial fails, we ask the
// peer to dial us instead (eg we can't reach a NAT'ed VM but it can reach us).
func (s *Server) weDialLocked(peer Peer, peerData PeerData) bool {
	ours := fmt.Sprintf("%s %q %s", s.idStr, s.Name, s.runID)
	theirs := fmt.Sprintf("%s %q %s", peer.PublicKey, peer.Name, peerData.RunID)
	return ours < theirs
}

// Linked returns true if the encrypted TCP link to peer is up.
func (s *Server) Linked(peer Peer) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	l := s.links[peer]
	return l != nil && l.conn != nil
}

func (s *Server) startTCP(localIP *net.UDPAddr) error {
	var laddr *net.TCPAddr
	if localIP != nil {
		laddr = &net.TCPAddr{IP: localIP.IP}
	}
	var err error
	s.tcpListen, err = net.ListenTCP("tcp4", laddr)
	if err != nil {
		return err
	}
	s.tcpPort = s.tcpListen.Addr().(*net.TCPAddr).Port
	return nil
}

func (s *Server) runTCPAccept(ctx context.Context) {
	defer s.wg.Done()
	log.Infof("Starting TCP link listener on %s", s.tcpListen.Addr())
	for {
		conn, err := s.tcpListen.Accept()
		if err != nil {
			if ctx.Err() != nil {
				log.Infof("Exiting TCP listener after %v", ctx.Err())
				return
			}
			log.Errf("Error accepting TCP connection: %v", err)
			continue
		}
		s.wg.Add(1)
		go s.handleIncoming(ctx, conn)
	}
}

// startDialLocked starts dialing peer's TCP link if we're the dialer.
func (s *Server) startDialLocked(peer Peer, peerData PeerData, l *link) {
	if !s.weDialLocked(peer, peerData) {
		log.LogVf("Waiting for %s to dial us", peer.Name)
		// Don't stay Connected without a link if they fail to dial us (eg firewall).
		time.AfterFunc(2*linkHelloTimeout, func() { s.linkTimeout(peer, l) })
		return
	}
	s.goDialLocked(peer, l, false)
}

// goDialLocked starts dialing the peer's TCP link. fallback is when the peer asked us to (its dial failed).
func (s *Server) goDialLocked(peer Peer, l *link, fallback bool) {
	if s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	addr := net.JoinHostPort(peer.IP, strconv.Itoa(l.tcpPort))
	if s.testDialAddr != nil {
		addr = s.testDialAddr(addr, fallback)
	}
	s.wg.Add(1)
	go s.dial(s.ctx, peer, l, addr, fallback)
}

// handleDialMe handles the peer's request to dial it (its dial to us failed).
func (s *Server) handleDialMe(from *net.UDPAddr, fromName, toName string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	peer, peerData, _, ok := s.senderLocked(from, fromName, toName)
	if !ok {
		return
	}
	l := s.links[peer]
	if peerData.Status != Connected || l == nil || l.session == nil || l.conn != nil {
		log.Warnf("Ignoring dial request from %s in state %v", peer.Name, peerData.Status)
		return
	}
	log.Infof("%s couldn't reach us, dialing it instead", peer.Name)
	s.goDialLocked(peer, l, true)
}

func (s *Server) dial(ctx context.Context, peer Peer, l *link, addr string, fallback bool) {
	defer s.wg.Done()
	log.Infof("Dialing TCP link to %s at %s", peer.Name, addr)
	dialer := net.Dialer{Timeout: linkDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(linkWriteTimeout))
		err = writeFrame(conn, l.session, FrameHello, nil)
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		if fallback {
			log.Errf("TCP link to %s (%s) failed: %v", peer.Name, addr, err)
			s.linkFailed(peer, l)
			return
		}
		log.Warnf("TCP link to %s (%s) failed: %v - asking it to dial us instead", peer.Name, addr, err)
		s.askDialMe(peer, l)
		return
	}
	if !s.attach(peer, l, conn) {
		return
	}
	s.sendLastClip(peer, l, conn)
	s.sendOffers(peer, l, conn)
	s.sendSyncFolders(peer, l, conn)
	s.readLoop(ctx, peer, l, conn)
}

// askDialMe asks the peer to dial us, and gives up if it didn't in time.
func (s *Server) askDialMe(peer Peer, l *link) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.links[peer] != l {
		return
	}
	peerData, ok := s.Peers.Get(peer)
	if !ok {
		return
	}
	if err := s.sendDirect(peer, peerData, fmt.Sprintf(DialMeMessageFormat, s.Name, peer.Name)); err != nil {
		log.Errf("Failed to ask %s to dial us: %v", peer.Name, err)
	}
	time.AfterFunc(2*linkHelloTimeout, func() { s.linkTimeout(peer, l) })
}

// attach sets the connection on the link if it is still current (and not already up).
func (s *Server) attach(peer Peer, l *link, conn net.Conn) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.links[peer] != l || l.conn != nil {
		log.Infof("Stale or duplicate TCP link to %s, closing", peer.Name)
		_ = conn.Close()
		return false
	}
	l.conn = conn
	l.outgoing, l.incoming = make(map[uint32]*Transfer), make(map[uint32]*recvFile)
	log.Infof("TCP link up with %s (%s)", peer.Name, conn.RemoteAddr())
	if peerData, ok := s.Peers.Get(peer); ok {
		s.change(s.Peers.Set(peer, peerData)) // just to bump version for UI refresh.
	}
	return true
}

// linkFailed resets peer to NotLinked if l is still its current link.
func (s *Server) linkFailed(peer Peer, l *link) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.links[peer] != l {
		return
	}
	s.resetLinkLocked(peer)
}

func (s *Server) resetLinkLocked(peer Peer) {
	s.dropLinkLocked(peer)
	if peerData, ok := s.Peers.Get(peer); ok {
		s.setStatusLocked(peer, peerData, NotLinked)
	}
}

func (s *Server) linkTimeout(peer Peer, l *link) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.links[peer] != l || l.conn != nil {
		return
	}
	log.Warnf("%s didn't establish the TCP link in time", peer.Name)
	s.resetLinkLocked(peer)
}

// handleIncoming authenticates an incoming TCP connection: its first frame must
// open with the session of one of the Connected peers at that IP (that we expect
// to dial us). Trying a session with the wrong frame doesn't change its state.
func (s *Server) handleIncoming(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(linkHelloTimeout)
	_ = conn.SetReadDeadline(deadline)
	remoteIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	frame, err := readFrame(conn)
	if err != nil {
		log.Warnf("Failed to read hello from %v: %v", conn.RemoteAddr(), err)
		_ = conn.Close()
		return
	}
	for {
		peer, l, found, bad := s.matchHello(remoteIP, frame, conn)
		if bad {
			return
		}
		if found {
			_ = conn.SetReadDeadline(time.Time{})
			s.sendLastClip(peer, l, conn)
			s.sendOffers(peer, l, conn)
			s.sendSyncFolders(peer, l, conn)
			s.readLoop(ctx, peer, l, conn)
			return
		}
		// Our side of the handshake may not have completed yet (eg accept still in flight).
		if time.Now().After(deadline) || ctx.Err() != nil {
			log.Warnf("Unauthenticated TCP connection from %v, closing", conn.RemoteAddr())
			_ = conn.Close()
			return
		}
		time.Sleep(helloRetryDelay)
	}
}

// matchHello returns the peer and link matching the hello frame if found, or bad
// if the frame was from a peer but invalid (and the connection got closed).
func (s *Server) matchHello(remoteIP string, frame []byte, conn net.Conn) (Peer, *link, bool, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for peer, l := range s.links {
		if peer.IP != remoteIP || l.session == nil || l.conn != nil {
			continue
		}
		peerData, ok := s.Peers.Get(peer)
		if !ok || peerData.Status != Connected { // either side may dial (fallback when one can't).
			continue
		}
		plain, err := l.session.Open(frame)
		if err != nil {
			continue
		}
		if len(plain) == 0 || plain[0] != FrameHello {
			log.Warnf("Unexpected first frame type from %s", peer.Name)
			_ = conn.Close()
			return peer, nil, false, true
		}
		l.conn = conn
		l.outgoing, l.incoming = make(map[uint32]*Transfer), make(map[uint32]*recvFile)
		log.Infof("TCP link up with %s (%s)", peer.Name, conn.RemoteAddr())
		s.change(s.Peers.Set(peer, peerData)) // bump version for UI refresh.
		return peer, l, true, false
	}
	return Peer{}, nil, false, false
}

func (s *Server) readLoop(ctx context.Context, peer Peer, l *link, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer s.linkClosed(l)
	for {
		frame, err := readFrame(conn)
		var plain []byte
		if err == nil {
			plain, err = l.session.Open(frame)
		}
		if err == nil && len(plain) == 0 {
			err = errors.New("empty frame")
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Warnf("TCP link with %s closed: %v", peer.Name, err)
			}
			_ = conn.Close()
			s.linkFailed(peer, l)
			return
		}
		switch plain[0] {
		case FrameClipNoTime:
			s.receivedClip(peer, l, string(plain[1:]), time.Now())
		case FrameClip:
			if len(plain) < 9 {
				log.Warnf("Invalid clipboard frame from %s (%d bytes)", peer.Name, len(plain))
				continue
			}
			when := time.Unix(0, int64(binary.BigEndian.Uint64(plain[1:9]))) //nolint:gosec // any value is fine.
			s.receivedClip(peer, l, string(plain[9:]), when)
		default:
			if !s.handleFileFrame(peer, l, conn, plain) && !s.handleSyncFrame(peer, l, conn, plain) {
				log.Warnf("Ignoring unknown frame type %q from %s", plain[0], peer.Name)
			}
		}
	}
}

func (s *Server) receivedClip(peer Peer, l *link, clip string, when time.Time) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.links[peer] != l {
		return
	}
	peerData, ok := s.Peers.Get(peer)
	if !ok {
		return
	}
	// Resent on reconnect (or an older one racing with a newer one): keep the latest.
	if !peerData.ClipTime.IsZero() && !when.After(peerData.ClipTime) {
		log.LogVf("Ignoring not newer clipboard from %s (%v vs %v)", peer.Name, when, peerData.ClipTime)
		return
	}
	log.Infof("Received %d bytes clipboard from %s", len(clip), peer.Name)
	peerData.Clip = clip
	peerData.ClipTime = when
	s.change(s.Peers.Set(peer, peerData))
}

func clipPayload(clip string, when time.Time) []byte {
	payload := make([]byte, 8, 8+len(clip))
	binary.BigEndian.PutUint64(payload, uint64(when.UnixNano()))
	return append(payload, clip...)
}

// sendLastClip sends our last clipboard (if any) to a newly linked peer.
func (s *Server) sendLastClip(peer Peer, l *link, conn net.Conn) {
	s.stateMu.Lock()
	clip, when := s.lastClip, s.lastClipTime
	s.stateMu.Unlock()
	if when.IsZero() {
		return
	}
	if err := l.send(conn, FrameClip, clipPayload(clip, when)); err != nil {
		log.Errf("Failed to send last clipboard to %s: %v", peer.Name, err)
		return
	}
	log.Infof("Sent last clipboard (%d bytes) to newly linked %s", len(clip), peer.Name)
}

// SendClip sends the clipboard text to all the peers we have a TCP link with.
// It is also remembered and sent to peers that get linked later.
// Returns how many peers it was sent to.
func (s *Server) SendClip(clip string) (int, error) {
	if len(clip) > MaxClipSize {
		return 0, fmt.Errorf("clipboard too large: %d > %d", len(clip), MaxClipSize)
	}
	now := time.Now()
	payload := clipPayload(clip, now)
	s.stateMu.Lock()
	s.lastClip, s.lastClipTime = clip, now
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
	sent := 0
	var errs []error
	for _, t := range targets {
		if err := t.l.send(t.conn, FrameClip, payload); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.peer.Name, err))
			continue
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// send writes one frame; sealing and writing are done under the same lock so the
// frames go out in nonce order.
func (l *link) send(conn net.Conn, frameType byte, payload []byte) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(linkWriteTimeout))
	err := writeFrame(conn, l.session, frameType, payload)
	if err != nil {
		_ = conn.Close() // read loop will notice and clean up.
	}
	return err
}
