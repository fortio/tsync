package tsnet

import (
	"crypto/ed25519"
	"fmt"
	"net"
	"sync"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/tcrypto"
)

// Handshake: direct (unicast UDP) messages carrying our ephemeral key signed with our
// identity, verified against the identity key the peer advertised in discovery. The
// user verifies the peer's human hash and accepts on first connection (TOFU), after
// which the peer's key is trusted and later connections are accepted automatically.

// link is the per peer handshake/session state. Only accessed with stateMu held.
type link struct {
	eph *tcrypto.Ephemeral // ours for the current handshake.
	// session is set once Connected, or while ReceivedConn (verified but pending user acceptance).
	session *tcrypto.Session
	tcpPort int      // peer's TCP link port (from its handshake message).
	conn    net.Conn // TCP link once up (set once).
	writeMu sync.Mutex
	// Connect request retries (while SentConn).
	sentAt time.Time
	tries  int
	// File transfers, created when the TCP link is up: outgoing is accessed with stateMu
	// held, incoming only by the link's read loop.
	outgoing map[uint32]*Transfer
	incoming map[uint32]*recvFile
}

const (
	// HandshakeRetryDelay is how long we wait for an answer before resending a connect request.
	HandshakeRetryDelay = 2 * time.Second
	// HandshakeMaxTries is how many times we send a connect request (we then stay in SentConn,
	// waiting for an accept, eg the other user may take a while to accept).
	HandshakeMaxTries = 5
	// AutoConnectInterval is the minimum time between automatic connection attempts to a trusted peer.
	AutoConnectInterval = 10 * time.Second
)

func (s ConnectionStatus) String() string {
	switch s {
	case NotLinked:
		return "NotLinked"
	case SentConn:
		return "SentConn"
	case ReceivedConn:
		return "ReceivedConn"
	case Connected:
		return "Connected"
	case Failed:
		return "Failed"
	default:
		return fmt.Sprintf("ConnectionStatus(%d)", int(s))
	}
}

// linkContext is the handshake context, identical on both sides. It includes both
// UDP ports, which are different for each run, so a signed ephemeral key captured
// from a previous run can't be replayed (eg by someone advertising the peer's
// public key from another address, as discovery itself isn't authenticated).
func (s *Server) linkContext(peer Peer, peerData PeerData) string {
	ours := fmt.Sprintf("%q:%d", s.Name, s.ourSendAddr.Port)
	theirs := fmt.Sprintf("%q:%d", peer.Name, peerData.Port)
	if theirs < ours {
		ours, theirs = theirs, ours
	}
	return "link1 " + ours + " " + theirs
}

// ConnectToPeer initiates a connection to the specified peer, or accepts the
// pending connection request from that peer if there is one (ReceivedConn).
func (s *Server) ConnectToPeer(peer Peer) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	peerData, exists := s.Peers.Get(peer)
	if !exists {
		return fmt.Errorf("peer %v not found (anymore) in peer list", peer)
	}
	if peerData.Status == ReceivedConn {
		return s.acceptLocked(peer, peerData)
	}
	if peerData.Status == Connected {
		log.Infof("Already connected to %s, reconnecting", peer.Name)
	}
	return s.connectLocked(peer, peerData)
}

func (s *Server) connectLocked(peer Peer, peerData PeerData) error {
	eph, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		return err
	}
	l := &link{eph: eph, sentAt: time.Now(), tries: 1}
	s.setLinkLocked(peer, l)
	if err = s.sendHandshakeLocked(ConnectMessageFormat, peer, peerData, eph); err != nil {
		s.setStatusLocked(peer, peerData, Failed)
		return err
	}
	s.setStatusLocked(peer, peerData, SentConn)
	log.Infof("Connection request sent to %s (%s)", peer.Name, peer.IP)
	return nil
}

// autoConnectLocked connects to peer if it's trusted, not linked and we didn't try recently.
func (s *Server) autoConnectLocked(peer Peer, peerData PeerData, now time.Time) {
	if s.NoAutoConnect || (peerData.Status != NotLinked && peerData.Status != Failed) || !s.isTrustedLocked(peer) {
		return
	}
	if now.Sub(s.lastAuto[peer]) < AutoConnectInterval {
		return
	}
	s.lastAuto[peer] = now
	log.Infof("Auto connecting to trusted peer %s (%s)", peer.Name, peerData.HumanHash)
	if err := s.connectLocked(peer, peerData); err != nil {
		log.Errf("Auto connect to %s failed: %v", peer.Name, err)
	}
}

// handshakeTick is called periodically: resends pending connect requests and auto connects trusted peers.
func (s *Server) handshakeTick() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	now := time.Now()
	for _, kv := range s.Peers.KeysValuesSnapshot() {
		peer, peerData := kv.Key, kv.Value
		l := s.links[peer]
		if peerData.Status == SentConn && l != nil && l.tries < HandshakeMaxTries && now.Sub(l.sentAt) >= HandshakeRetryDelay {
			l.tries++
			l.sentAt = now
			log.Infof("No answer yet from %s, resending connection request (%d/%d)", peer.Name, l.tries, HandshakeMaxTries)
			if err := s.sendHandshakeLocked(ConnectMessageFormat, peer, peerData, l.eph); err != nil {
				log.Errf("Failed to resend connection request to %s: %v", peer.Name, err)
			}
			continue
		}
		s.autoConnectLocked(peer, peerData, now)
	}
}

func (s *Server) setStatusLocked(peer Peer, peerData PeerData, status ConnectionStatus) {
	peerData.Status = status
	s.change(s.Peers.Set(peer, peerData))
}

// dropLinkLocked forgets any handshake/session state for peer (eg peer restarted or expired).
func (s *Server) dropLinkLocked(peer Peer) {
	s.setLinkLocked(peer, nil)
}

// setLinkLocked replaces (or deletes when l is nil) the peer's link, closing the previous TCP link if any.
func (s *Server) setLinkLocked(peer Peer, l *link) {
	if old := s.links[peer]; old != nil && old != l && old.conn != nil {
		_ = old.conn.Close()
	}
	if l == nil {
		delete(s.links, peer)
		return
	}
	s.links[peer] = l
}

func (s *Server) sendHandshakeLocked(format string, peer Peer, peerData PeerData, eph *tcrypto.Ephemeral) error {
	signed := s.Identity.SignEphemeral(eph, s.linkContext(peer, peerData))
	return s.sendDirect(peer, peerData, fmt.Sprintf(format, s.Name, peer.Name, s.tcpPort, signed))
}

func (s *Server) sendDirect(peer Peer, peerData PeerData, message string) error {
	if len(message) > BufSize {
		return fmt.Errorf("message to %s too long (%d > %d), names too long?", peer.Name, len(message), BufSize)
	}
	directPeerAddr := &net.UDPAddr{
		IP:   net.ParseIP(peer.IP),
		Port: peerData.Port, // use the same port as discovery
	}
	_, err := s.dualUDPSock.WriteToUDP([]byte(message), directPeerAddr)
	return err
}

func (s *Server) sendReject(peer Peer, peerData PeerData, reason string) {
	if err := s.sendDirect(peer, peerData, fmt.Sprintf(RejectMessageFormat, s.Name, peer.Name, reason)); err != nil {
		log.Errf("Failed to send reject to %s: %v", peer.Name, err)
	}
}

// acceptLocked sends our accept for the already verified pending request and marks the peer Connected.
func (s *Server) acceptLocked(peer Peer, peerData PeerData) error {
	l := s.links[peer]
	if l == nil || l.session == nil {
		return fmt.Errorf("no pending connection request from %s", peer.Name)
	}
	if err := s.sendHandshakeLocked(AcceptMessageFormat, peer, peerData, l.eph); err != nil {
		return err
	}
	s.connectedLocked(peer, peerData)
	return nil
}

func (s *Server) connectedLocked(peer Peer, peerData PeerData) {
	s.setStatusLocked(peer, peerData, Connected)
	log.Infof("Connected to %s (%s %s)", peer.Name, peer.IP, peerData.HumanHash)
	s.addTrustedLocked(peer)
	if l := s.links[peer]; l != nil {
		s.startDialLocked(peer, peerData, l)
	}
}

func (s *Server) isTrustedLocked(peer Peer) bool {
	_, ok := s.trusted[peer.PublicKey]
	return ok
}

func (s *Server) addTrustedLocked(peer Peer) {
	if name, ok := s.trusted[peer.PublicKey]; ok && name == peer.Name {
		return
	}
	s.trusted[peer.PublicKey] = peer.Name
	if s.Storage == nil {
		return
	}
	if err := s.Storage.AddTrusted(peer.PublicKey, peer.Name); err != nil {
		log.Errf("Failed to save trusted peer %s: %v", peer.Name, err)
	}
}

// handleDirectMessage processes incoming direct (handshake) messages.
func (s *Server) handleDirectMessage(buf []byte, from *net.UDPAddr) {
	msgStr := string(buf)
	var fromName, toName, arg string
	var tcpPort int
	if n, err := fmt.Sscanf(msgStr, ConnectMessageFormat, &fromName, &toName, &tcpPort, &arg); err == nil && n == 4 {
		s.handleConnect(from, fromName, toName, tcpPort, arg)
		return
	}
	if n, err := fmt.Sscanf(msgStr, AcceptMessageFormat, &fromName, &toName, &tcpPort, &arg); err == nil && n == 4 {
		s.handleAccept(from, fromName, toName, tcpPort, arg)
		return
	}
	if n, err := fmt.Sscanf(msgStr, RejectMessageFormat, &fromName, &toName, &arg); err == nil && n == 3 {
		s.handleReject(from, fromName, toName, arg)
		return
	}
	log.Warnf("Unknown direct message format from %v: %q", from, msgStr)
}

// senderLocked finds the discovered peer matching the source address and names of a direct message.
func (s *Server) senderLocked(from *net.UDPAddr, fromName, toName string) (Peer, PeerData, ed25519.PublicKey, bool) {
	src := Source{IP: from.IP.String(), Port: from.Port}
	peer, exists := s.Sources.Get(src)
	if !exists {
		log.Errf("Direct message from unknown source %v (not in source to peer map)", src)
		return peer, PeerData{}, nil, false
	}
	peerData, found := s.Peers.Get(peer)
	if !found {
		log.Errf("Direct message from unknown peer %v (not in discovery map)", peer)
		return peer, peerData, nil, false
	}
	if fromName != peer.Name || toName != s.Name {
		log.Warnf("Direct message from %v names mismatch: %q -> %q (expected %q -> %q)", src, fromName, toName, peer.Name, s.Name)
		return peer, peerData, nil, false
	}
	pub, err := tcrypto.IdentityPublicKeyString(peer.PublicKey)
	if err != nil {
		log.Errf("Invalid public key for peer %v: %v", peer, err)
		return peer, peerData, nil, false
	}
	return peer, peerData, pub, true
}

func (s *Server) handleConnect(from *net.UDPAddr, fromName, toName string, tcpPort int, signed string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	log.Infof("Received connection request from %v: %v to %v", from, fromName, toName)
	peer, peerData, pub, ok := s.senderLocked(from, fromName, toName)
	if !ok {
		return
	}
	l := s.links[peer]
	// Both sides clicked: we already have our ephemeral key out, use it.
	simultaneous := peerData.Status == SentConn && l != nil
	if !simultaneous {
		eph, err := tcrypto.NewEphemeralKeys()
		if err != nil {
			log.Errf("Failed to create ephemeral keys: %v", err)
			return
		}
		l = &link{eph: eph}
	}
	session, err := s.Identity.VerifiedSession(l.eph, signed, s.linkContext(peer, peerData), pub)
	if err != nil {
		log.Errf("Invalid connection request from %s (%s): %v", peer.Name, peer.IP, err)
		s.sendReject(peer, peerData, "invalid signature")
		return
	}
	l.session = session
	l.tcpPort = tcpPort
	s.setLinkLocked(peer, l)
	switch {
	case simultaneous:
		log.Infof("Simultaneous connection with %s", peer.Name)
		// Also send our accept in case our own connect request got lost.
		if err = s.acceptLocked(peer, peerData); err != nil {
			log.Errf("Failed to send accept to %s: %v", peer.Name, err)
		}
	case s.isTrustedLocked(peer):
		log.Infof("Auto accepting connection from trusted %s (%s)", peer.Name, peerData.HumanHash)
		if err = s.acceptLocked(peer, peerData); err != nil {
			log.Errf("Failed to accept connection from %s: %v", peer.Name, err)
		}
	default:
		if peerData.Status != ReceivedConn { // only once, not for each retry.
			log.Warnf("Connection request from new peer %s (%s): check its hash is %s and select it to accept",
				peer.Name, peer.IP, peerData.HumanHash)
		}
		s.setStatusLocked(peer, peerData, ReceivedConn)
	}
}

func (s *Server) handleAccept(from *net.UDPAddr, fromName, toName string, tcpPort int, signed string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	log.Infof("Received connection accept from %v: %v to %v", from, fromName, toName)
	peer, peerData, pub, ok := s.senderLocked(from, fromName, toName)
	if !ok {
		return
	}
	l := s.links[peer]
	if peerData.Status != SentConn || l == nil {
		if peerData.Status == Connected {
			log.LogVf("Ignoring accept from already connected %s", peer.Name)
		} else {
			log.Warnf("Unexpected accept from %s in state %v", peer.Name, peerData.Status)
		}
		return
	}
	session, err := s.Identity.VerifiedSession(l.eph, signed, s.linkContext(peer, peerData), pub)
	if err != nil {
		log.Errf("Invalid connection accept from %s (%s): %v", peer.Name, peer.IP, err)
		return
	}
	l.session = session
	l.tcpPort = tcpPort
	s.connectedLocked(peer, peerData)
}

func (s *Server) handleReject(from *net.UDPAddr, fromName, toName, reason string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	peer, peerData, _, ok := s.senderLocked(from, fromName, toName)
	if !ok {
		return
	}
	log.Warnf("Connection rejected by %s: %s", peer.Name, reason)
	if peerData.Status == SentConn {
		s.dropLinkLocked(peer)
		s.setStatusLocked(peer, peerData, Failed)
	}
}
