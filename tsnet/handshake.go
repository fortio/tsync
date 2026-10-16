package tsnet

import (
	"crypto/ed25519"
	"fmt"
	"net"

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
}

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

// linkContext is the handshake context, identical on both sides.
func linkContext(name1, name2 string) string {
	if name2 < name1 {
		name1, name2 = name2, name1
	}
	return fmt.Sprintf("link1 %q %q", name1, name2)
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
	eph, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		return err
	}
	s.links[peer] = &link{eph: eph}
	if err = s.sendHandshakeLocked(ConnectMessageFormat, peer, peerData, eph); err != nil {
		s.setStatusLocked(peer, peerData, Failed)
		return err
	}
	s.setStatusLocked(peer, peerData, SentConn)
	log.Infof("Connection request sent to %s (%s)", peer.Name, peer.IP)
	return nil
}

func (s *Server) setStatusLocked(peer Peer, peerData PeerData, status ConnectionStatus) {
	peerData.Status = status
	s.change(s.Peers.Set(peer, peerData))
}

// dropLinkLocked forgets any handshake/session state for peer (eg peer restarted or expired).
func (s *Server) dropLinkLocked(peer Peer) {
	delete(s.links, peer)
}

func (s *Server) sendHandshakeLocked(format string, peer Peer, peerData PeerData, eph *tcrypto.Ephemeral) error {
	signed := s.Identity.SignEphemeral(eph, linkContext(s.Name, peer.Name))
	return s.sendDirect(peer, peerData, fmt.Sprintf(format, s.Name, peer.Name, signed))
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
	if n, err := fmt.Sscanf(msgStr, ConnectMessageFormat, &fromName, &toName, &arg); err == nil && n == 3 {
		s.handleConnect(from, fromName, toName, arg)
		return
	}
	if n, err := fmt.Sscanf(msgStr, AcceptMessageFormat, &fromName, &toName, &arg); err == nil && n == 3 {
		s.handleAccept(from, fromName, toName, arg)
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

func (s *Server) handleConnect(from *net.UDPAddr, fromName, toName, signed string) {
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
	session, err := s.Identity.VerifiedSession(l.eph, signed, linkContext(s.Name, peer.Name), pub)
	if err != nil {
		log.Errf("Invalid connection request from %s (%s): %v", peer.Name, peer.IP, err)
		s.sendReject(peer, peerData, "invalid signature")
		return
	}
	l.session = session
	s.links[peer] = l
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
		s.setStatusLocked(peer, peerData, ReceivedConn)
		log.Infof("Connection request from new peer %s (%s): check its hash is %s and select it to accept",
			peer.Name, peer.IP, peerData.HumanHash)
	}
}

func (s *Server) handleAccept(from *net.UDPAddr, fromName, toName, signed string) {
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
	session, err := s.Identity.VerifiedSession(l.eph, signed, linkContext(s.Name, peer.Name), pub)
	if err != nil {
		log.Errf("Invalid connection accept from %s (%s): %v", peer.Name, peer.IP, err)
		return
	}
	l.session = session
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
