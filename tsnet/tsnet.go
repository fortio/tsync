// Package tsnet is the network library support for tsync (discovery/registration and communication).
package tsnet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fortio.org/log"
	"fortio.org/smap"
	"fortio.org/tsync/tcrypto"
	"golang.org/x/net/ipv4"
)

const (
	// BufSize is the max size of messages (safe size).
	// 576 byte IP packet - 60 byte IP header - 8 byte UDP header = 508 bytes.
	BufSize = 576 - 60 - 8
	// DefaultTarget: which udp address we try by default to find our interface and ip.
	DefaultTarget                  = "8.8.8.8:53"
	DefaultBroadcastInterval       = 1500 * time.Millisecond
	TimeFormat                     = "15:04:05.000" // time only + millis.
	DefaultPeerTimeout             = 10 * time.Second
	epochStopMarker          int32 = -999
	DefaultDiscoveryPort           = 29556 // "ts" in ascii.
)

type Config struct {
	// Name to use, if empty hostname will be used.
	Name  string
	Port  int
	Mcast string
	// Which ip:port we try to resolve to find our address and interface.
	Target string
	// Callback called when a the Server Peers map has changed, a new peer is detected
	// or old one removed or updated. Must not block for long or
	// it will delay processing of incoming messages.
	OnChange              func(version uint64)
	Identity              *tcrypto.Identity // long term identity for this server
	BaseBroadcastInterval time.Duration     // default to 1.5s if 0
	PeerTimeout           time.Duration     // default to 10s if 0
}

type ConnectionStatus int

const (
	Connecting ConnectionStatus = iota + 1
	ConnSent
	Connected
	Disconnected
	Failed
)

type Connection struct {
	Peer      Peer
	Status    ConnectionStatus
	CreatedAt time.Time
	Conn      *net.UDPConn
}

type Server struct {
	// Our copy of the input config.
	Config
	// internal state
	ourSendAddr     *net.UDPAddr
	destAddr        *net.UDPAddr
	broadcastListen *net.UDPConn
	dualUDPSock     *net.UDPConn // used for both sending (to multicast/unicast) and receiving (unicast)
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	Peers           *smap.Map[Peer, PeerData]
	idStr           string
	epoch           atomic.Int32                // set to negative when stopped, panics after 2B ticks/if it wraps.
	connections     *smap.Map[Peer, Connection] // peer -> Connection
}

type Peer struct {
	IP        string
	Name      string
	PublicKey string
}

type PeerData struct {
	HumanHash string
	Port      int
	Epoch     int32
	LastSeen  time.Time
}

func (c *Config) NewServer() *Server {
	return &Server{
		Config:      *c,
		Peers:       smap.New[Peer, PeerData](),
		connections: smap.New[Peer, Connection](),
	}
}

func (s *Server) Start(ctx context.Context) error {
	s.idStr = s.Identity.PublicKeyToString()
	var err error
	if s.Name == "" {
		s.Name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	if s.BaseBroadcastInterval <= 0 {
		s.BaseBroadcastInterval = DefaultBroadcastInterval
	}
	if s.PeerTimeout <= 0 {
		s.PeerTimeout = DefaultPeerTimeout
	}
	if s.Target == "" {
		s.Target = DefaultTarget
	}
	if strings.IndexByte(s.Target, ':') < 0 {
		s.Target += ":53" // default to dns port (even though we don't really use the port for target)
	}
	addr := fmt.Sprintf("%s:%d", s.Mcast, s.Port)
	s.destAddr, err = net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	log.Infof("Starting tsync server %q on %s -> %s", s.Name, addr, s.destAddr)
	// Try to get the right interface to listen on
	goodIf, localIP, err := GetInternetInterface(ctx, s.Target)
	if err != nil {
		log.Warnf("Could not get default route interface using %q as test destination, will listen on all: %v", s.Target, err)
	} else {
		log.Infof("Using interface %q for multicast (with local IP %v)", goodIf.Name, localIP)
	}
	s.broadcastListen, err = net.ListenMulticastUDP("udp4", goodIf, s.destAddr)
	if err != nil {
		return err
	}
	// Enable multicast loopback so we can see our own packets (needed on Windows)
	p := ipv4.NewPacketConn(s.broadcastListen)
	if err = p.SetMulticastLoopback(true); err != nil {
		log.Warnf("Failed to enable multicast loopback: %v", err)
	}
	s.dualUDPSock, err = net.ListenUDP("udp4", localIP) // was net.DialUDP("udp4", localIP, s.destAddr)
	if err != nil {
		s.broadcastListen.Close()
		return err
	}
	s.ourSendAddr = s.dualUDPSock.LocalAddr().(*net.UDPAddr)
	log.Infof("Sockets created - unicast: %s, multicast listen: %s",
		s.ourSendAddr, s.broadcastListen.LocalAddr())

	// get a cancelable context
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(3) // broadcast sender, multicast receiver, and unicast receiver
	go s.runAdv(ctx)
	go s.runMulticastReceive(ctx)
	go s.runUnicastReceive(ctx)
	return nil
}

func (s *Server) Stop() {
	if s.Stopped() {
		return
	}
	s.epoch.Store(epochStopMarker)
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.cancel = nil
	s.broadcastListen.Close() // needed or write will block forever
	s.dualUDPSock.Close()
	s.wg.Wait()
	// Close all active connections
	for _, conn := range s.connections.All() {
		if conn.Conn != nil {
			conn.Conn.Close()
		}
	}
}

func (s *Server) Stopped() bool {
	return s.epoch.Load() < 0 // we may stop with -999 and some extra Add(1) happens but stays negative.
}

func (s *Server) runAdv(ctx context.Context) {
	defer s.wg.Done()
	// broadcast interval + 1-1023 msec jitter
	jitter := 1 + rand.IntN(1024) //nolint:gosec // not cryptographic
	interval := s.BaseBroadcastInterval + time.Duration(jitter)*time.Millisecond
	ticker := time.NewTicker(interval)
	log.Infof("Starting tsync broadcast sender %q (%v) with %v interval (jitter %d ms)",
		s.Name, s.ourSendAddr, interval, jitter)
	defer ticker.Stop()
	epoch := s.epoch.Load()
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting tsync sender %q after %d ticks (%v)", s.Name, epoch, ctx.Err())
			return
		case <-ticker.C:
			newEpoch := s.epoch.Add(1)
			log.LogVf("Tick %d -> %d", epoch, newEpoch)
			if newEpoch < epochStopMarker {
				panic("ticks wrapped, server ran for over 2B ticks??")
			}
			if newEpoch < 0 {
				log.Infof("Server stopped, not sending message")
				return
			}
			epoch = newEpoch
			err := s.MessageSend(epoch)
			if err != nil {
				log.Errf("Error sending UDP packet: %v", err)
			}
			// Run some cleanup/expire entries
			s.PeersCleanup()
		}
	}
}

func (s *Server) PeersCleanup() {
	var toDelete []Peer
	now := time.Now()
	for peer, data := range s.Peers.All() {
		if now.Sub(data.LastSeen) > s.PeerTimeout {
			toDelete = append(toDelete, peer)
		}
	}
	if len(toDelete) > 0 {
		log.Infof("Removing %d expired peers: %v", len(toDelete), toDelete)
		s.Peers.Delete(toDelete...)
	}
}

func (s *Server) OurAddress() *net.UDPAddr {
	return s.ourSendAddr
}

// Connections returns the connections map for testing/inspection.
func (s *Server) Connections() *smap.Map[Peer, Connection] {
	return s.connections
}

func (s *Server) change(version uint64) {
	if s.OnChange != nil {
		s.OnChange(version)
	}
}

// runUnicastReceive handles incoming unicast messages (direct peer connections).
func (s *Server) runUnicastReceive(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, BufSize)
	log.Infof("Starting unicast receiver %q on %s with %d bytes buffer",
		s.Name, s.dualUDPSock.LocalAddr(), BufSize)
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting unicast receiver after %v", ctx.Err())
			return
		default:
			// we rely on Stop() closing the socket to unblock ReadFromUDP on exit.
			n, addr, err := s.dualUDPSock.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					log.Infof("Normal unicast read error on exit: %v", err)
				} else {
					log.Errf("Error receiving unicast packet: %v", err)
				}
				continue
			}
			// Unicast messages are always from other peers, never from ourselves
			log.LogVf("Received unicast message %d bytes from %v: %q", n, addr, buf[:n])
			// Process as direct message
			s.handleDirectMessage(buf[:n], addr)
		}
	}
}

func (s *Server) runMulticastReceive(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, BufSize)
	log.Infof("Starting tsync broadcast receiver %q on %s with %d bytes buffer",
		s.Name, s.broadcastListen.LocalAddr(), BufSize)
	ourAddr := s.ourSendAddr
	us := Peer{Name: s.Name, IP: ourAddr.IP.String(), PublicKey: s.Identity.PublicKeyToString()}
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting tsync receiver after %v", ctx.Err())
			return
		default:
			// we rely on Stop() closing the socket to unblock ReadFromUDP on exit.
			n, addr, err := s.broadcastListen.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					log.Infof("Normal read from closed error on exit: %v", err)
				} else {
					log.Errf("Error receiving UDP packet: %v", err)
				}
				continue
			}
			if addr.IP.Equal(ourAddr.IP) && addr.Port == ourAddr.Port {
				log.Debugf("Ignoring our own packet (%q)", buf[:n])
				continue
			}
			log.LogVf("Received %d bytes from %v: %q", n, addr, buf[:n])
			name, pubKey, theirEpoch, err := s.MCastMessageDecode(buf[:n])
			if err != nil {
				log.Errf("Error decoding UDP packet %q from %v: %v", buf[:n], addr, err)
				continue
			}
			data := PeerData{Port: addr.Port, Epoch: theirEpoch, LastSeen: time.Now()}
			peer := Peer{Name: name, IP: addr.IP.String(), PublicKey: pubKey}
			if peer == us {
				if theirEpoch <= s.epoch.Load() {
					log.FErrf("Duplicate newer name,ip,pubkey detected... exiting (%v %v)", peer, data)
					s.Stop()
				} else {
					log.Warnf("Duplicate older name,ip,pubkey detected... ignoring - they should exit (%v %v)", peer, data)
				}
				continue
			}
			if v, ok := s.Peers.Get(peer); ok {
				log.S(log.Verbose, "Already known peer", log.Any("Peer", peer), log.Any("OldData", v), log.Any("NewData", data))
				// transfer the human hash (same pub key so same human hash)
				data.HumanHash = v.HumanHash
				// Check if this is an updated port
				if v.Port != data.Port {
					log.Infof("Peer %q port changed from %d to %d", peer, v.Port, data.Port)
				}
				// Update last seen and epoch
				s.change(s.Peers.Set(peer, data))
				continue
			}
			pub, err := tcrypto.IdentityPublicKeyString(peer.PublicKey)
			data.HumanHash = tcrypto.HumanHash(pub)
			if err != nil {
				log.Errf("Failed to decode peer %q public key %q: %v", peer.Name, peer.PublicKey, err)
				data.HumanHash = "BAD-PKEY"
			}
			nv := s.Peers.Set(peer, data)
			log.S(log.Info, "New peer", log.Any("count", s.Peers.Len()),
				log.Any("Peer", peer), log.Any("Data", data))
			s.change(nv)
		}
	}
}

// GetInternetInterface returns the interface used to reach a public IP (default route).
// Windows tend to pick somehow the wrong interface instead of listening to all/correct
// default one so we try to guess the right one by connecting to an external address.
func GetInternetInterface(ctx context.Context, target string) (*net.Interface, *net.UDPAddr, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "udp4", target)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	// clear the port as it's the current port for this test and not something useful to return.
	localAddr.Port = 0
	localIP := localAddr.IP
	log.Debugf("Local address used to reach %q is %v", target, localAddr)
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, iface := range interfaces {
		log.Debugf("Checking interface %q flags %v", iface.Name, iface.Flags)
		want := net.FlagUp | net.FlagMulticast | net.FlagRunning
		if iface.Flags&want != want {
			continue
		}
		// don't want:
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			log.Debugf("  Checking addr %q", addr.String())
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.IP.Equal(localIP) {
				return &iface, localAddr, nil
			}
		}
	}
	return nil, nil, errors.New("no default route interface found")
}

const (
	DiscoveryMessageFormat = "tsync1 %q %s e %d" // name, public key, epoch
	ConnectMessageFormat   = "connect1 %q %q"    // requester_name, target_name
	AcceptMessageFormat    = "accept1 %q"        // target_name
	RejectMessageFormat    = "reject1 %q %q"     // target_name, reason
	DataMessageFormat      = "data1 %q %s"       // target_name, signed_data
)

func (s *Server) MessageSend(epoch int32) error {
	payload := fmt.Sprintf(DiscoveryMessageFormat, s.Name, s.idStr, epoch)
	_, err := s.dualUDPSock.WriteToUDP([]byte(payload), s.destAddr)
	return err
}

func (s *Server) MCastMessageDecode(buf []byte) (string, string, int32, error) {
	var name string
	var pubKeyStr string
	var epoch int32
	n, err := fmt.Sscanf(string(buf), DiscoveryMessageFormat, &name, &pubKeyStr, &epoch)
	if err != nil {
		return "", "", 0, err
	}
	if n != 3 {
		return "", "", 0, fmt.Errorf("could not decode message %q", string(buf))
	}
	return name, pubKeyStr, epoch, nil
}

// PeerLess sort function for smap.AllSorted.
// Sorts by IP, then name, then public key.
func PeerLess(a, b Peer) bool {
	if a.IP != b.IP {
		return a.IP < b.IP
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.PublicKey < b.PublicKey
}

// PeerKVSort sort function for slices.SortFunc of smap.KV[Peer, PeerData].
// Sorts by IP, then name, then public key.
func PeerKVSort(a, b smap.KV[Peer, PeerData]) int {
	if PeerLess(a.Key, b.Key) {
		return -1
	}
	if a.Key == b.Key { // not actually possible for KV snapshot from a map.
		return 0
	}
	return 1
}

// ConnectToPeer initiates a connection to the specified peer.
func (s *Server) ConnectToPeer(peer Peer) error {
	// Create connection entry
	conn := Connection{
		Peer:      peer,
		Status:    Connecting,
		CreatedAt: time.Now(),
	}
	s.connections.Set(peer, conn)

	// Send connection request to peer's direct port
	peerData, exists := s.Peers.Get(peer)
	if !exists {
		return fmt.Errorf("peer %v not found in peer list", peer)
	}

	directPeerAddr := &net.UDPAddr{
		IP:   net.ParseIP(peer.IP),
		Port: peerData.Port, // use the same port as discovery
	}

	// Create UDP connection for direct communication
	udpConn, err := net.DialUDP("udp4", nil, directPeerAddr)
	if err != nil {
		return err
	}

	// Update connection with UDP conn
	conn.Conn = udpConn
	conn.Status = ConnSent
	// Re-set/replace, using the thread safety of the map for making this update race safe.
	s.connections.Set(peer, conn)

	// Send connection request
	message := fmt.Sprintf(ConnectMessageFormat, s.Name, peer.Name)
	_, err = udpConn.Write([]byte(message))
	if err != nil {
		udpConn.Close()
		s.connections.Delete(peer)
		return err
	}

	log.Infof("Connection request sent to %s (%s)", peer.Name, peer.IP)
	return nil
}

// handleDirectMessage processes incoming direct connection messages.
func (s *Server) handleDirectMessage(buf []byte, addr *net.UDPAddr) {
	msgStr := string(buf)

	// Try to parse as connection request
	var requesterName, targetName string
	if n, err := fmt.Sscanf(msgStr, ConnectMessageFormat, &requesterName, &targetName); err == nil && n == 2 {
		s.handleConnectionRequest(requesterName, targetName)
		return
	}

	log.Warnf("Unknown direct message format from %v: %q", addr, msgStr)
}

// handleConnectionRequest processes incoming connection requests.
func (s *Server) handleConnectionRequest(requesterName, targetName string) {
	log.Infof("Received connection request from %s to %s", requesterName, targetName)

	// Check if the target name matches our name
	if targetName != s.Name {
		log.Warnf("Connection request target name %q doesn't match our name %q", targetName, s.Name)
		return
	}
}
