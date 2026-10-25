// Package tsnet is the network library support for tsync (discovery/registration and communication).
package tsnet

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

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

// Compatible returns false for peers running an older tsync version we can't connect with.
func (pd PeerData) Compatible() bool {
	return pd.RunID != ""
}

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
	// Storage for the trusted peers (TOFU), if nil trust is only kept in memory.
	Storage *tcrypto.Storage
	// NoAutoConnect disables automatically connecting to trusted peers when discovered (or link lost).
	NoAutoConnect bool
	// FileDir is where received files are saved (current directory if empty).
	FileDir string
	// Peers are host[:port] addresses we also send our discovery message to directly (unicast),
	// for when multicast doesn't work (port defaults to Port).
	Peers []string
	// MaxRate limits the file sending rate in bytes per second, per peer link (0 for unlimited).
	MaxRate int64
	// Sync are the directories to sync with the linked peers that have ones with the same names.
	Sync []SyncFolder
	// SyncRescan is how often the sync folders are scanned (DefaultSyncRescan if 0).
	SyncRescan time.Duration
	// SyncKeep is how many replaced versions of each file are kept in the sync folders' OldDir (0 for none).
	SyncKeep int
	// SyncConflictList leaves the sync conflicts alone (listed) instead of resolving them with conflict copies.
	SyncConflictList bool
}

type ConnectionStatus int

const (
	// NotLinked is the initial state once discovered.
	NotLinked ConnectionStatus = iota
	// SentConn is the state when a connection is being established.
	SentConn
	// ReceivedConn is the state when a connection request has been received.
	ReceivedConn
	// Connected is the state when a connection has been established.
	Connected
	// Failed is the state when a connection has failed.
	Failed
)

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
	Sources         *smap.Map[Source, Peer] // maps ip,port to peer
	idStr           string
	epoch           atomic.Int32 // set to negative when stopped, panics after 2B ticks/if it wraps.
	// Handshake/session state: stateMu serializes all read-modify-write of Peers data (status) and links.
	stateMu sync.Mutex
	links   map[Peer]*link
	trusted map[string]string // trusted public keys -> name
	// last automatic connection attempt to trusted peers
	lastAuto map[Peer]time.Time
	// last clipboard we sent, also sent to newly linked peers.
	lastClip     string
	lastClipTime time.Time
	// file transfers
	transfers  []*Transfer
	nextFileID uint32
	offers     map[uint32]*offer   // our recent offers, by id.
	rdirs      map[dirKey]*recvDir // directories being downloaded.
	// sync folders by name.
	syncFolders map[string]*syncFolder
	nextSyncID  uint32 // for our sync downloads (with syncIDBit set).
	// resolved Config.Peers
	staticPeers []*net.UDPAddr
	// random id of this run, in our discovery messages (handshake context, restart detection).
	runID string
	// testDialAddr if set changes the address we dial (tests).
	testDialAddr func(addr string, fallback bool) string
	changes      atomic.Uint64 // OnChange version.
	// TCP links
	tcpListen *net.TCPListener
	tcpPort   int
	ctx       context.Context //nolint:containedctx // server lifetime context, for dial goroutines.
	// direct messages received before the sender's discovery, by source (see stashEarlyLocked).
	early map[Source]earlyMsg
}

type Source struct {
	IP   string
	Port int
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
	Status    ConnectionStatus
	RunID     string // random id of the peer's current run (from its discovery messages), empty for old versions.
	// Last clipboard received from that peer.
	Clip     string
	ClipTime time.Time
}

func (c *Config) NewServer() *Server {
	return &Server{
		Config:   *c,
		Peers:    smap.New[Peer, PeerData](),
		Sources:  smap.New[Source, Peer](),
		links:    make(map[Peer]*link),
		trusted:  make(map[string]string),
		lastAuto: make(map[Peer]time.Time),
		offers:   make(map[uint32]*offer),
		rdirs:    make(map[dirKey]*recvDir),
		// sync folders are set in Start.
		syncFolders: make(map[string]*syncFolder),
		early:       make(map[Source]earlyMsg),
		// Random start so offer ids from different runs don't collide (eg in peers' lists).
		// Below 2^30 so they never reach syncIDBit (even with the ids reserved by directory offers).
		nextFileID: rand.Uint32() >> 2, //nolint:gosec // not cryptographic.
	}
}

func (s *Server) Start(ctx context.Context) error {
	s.idStr = s.Identity.PublicKeyToString()
	s.runID = newRunID()
	var err error
	if s.Name == "" {
		s.Name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	if err = ValidateName(s.Name); err != nil {
		return err
	}
	if s.BaseBroadcastInterval <= 0 {
		s.BaseBroadcastInterval = DefaultBroadcastInterval
	}
	if s.PeerTimeout <= 0 {
		s.PeerTimeout = DefaultPeerTimeout
	}
	if s.Storage != nil {
		s.trusted, err = s.Storage.LoadTrusted()
		if err != nil {
			return err
		}
		log.Infof("Loaded %d trusted peers", len(s.trusted))
	}
	for _, p := range s.Config.Peers {
		if _, _, perr := net.SplitHostPort(p); perr != nil {
			p = net.JoinHostPort(p, strconv.Itoa(s.Port))
		}
		a, rerr := net.ResolveUDPAddr("udp4", p)
		if rerr != nil {
			return fmt.Errorf("invalid peer address %q: %w", p, rerr)
		}
		s.staticPeers = append(s.staticPeers, a)
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
	if err = s.startTCP(localIP); err != nil {
		s.broadcastListen.Close()
		s.dualUDPSock.Close()
		return err
	}
	log.Infof("Sockets created - unicast: %s, multicast listen: %s, tcp: %s",
		s.ourSendAddr, s.broadcastListen.LocalAddr(), s.tcpListen.Addr())

	// get a cancelable context
	ctx, s.cancel = context.WithCancel(ctx)
	s.stateMu.Lock()
	s.ctx = ctx
	s.startSync(ctx)
	s.stateMu.Unlock()
	s.wg.Add(4) // broadcast sender, multicast receiver, unicast receiver and tcp accept
	go s.runAdv(ctx)
	go s.runMulticastReceive(ctx)
	go s.runUnicastReceive(ctx)
	go s.runTCPAccept(ctx)
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
	s.tcpListen.Close() // TCP links get closed by the context cancel.
	s.wg.Wait()
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
			err := s.MCastMessageSend(epoch)
			if err != nil {
				log.Errf("Error sending UDP packet: %v", err)
			}
			s.directDiscovery(epoch)
			// Run some cleanup/expire entries
			s.PeersCleanup()
			s.handshakeTick()
		}
	}
}

func (s *Server) PeersCleanup() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	var toDelete []Peer
	var toDeleteSources []Source
	now := time.Now()
	for peer, data := range s.Peers.All() {
		if now.Sub(data.LastSeen) > s.PeerTimeout {
			toDelete = append(toDelete, peer)
			src := Source{IP: peer.IP, Port: data.Port}
			toDeleteSources = append(toDeleteSources, src)
		}
	}
	if len(toDelete) > 0 {
		log.Infof("Removing %d expired peers: %q", len(toDelete), toDelete)
		v := s.Peers.Delete(toDelete...)
		s.Sources.Delete(toDeleteSources...) // TODO share lock/transaction.
		for _, peer := range toDelete {
			s.dropLinkLocked(peer)
			delete(s.lastAuto, peer)
			s.expireOffersLocked(peer, "peer gone")
		}
		s.change(v) // UI refresh, otherwise the last peer to go away stays listed.
	}
	for src, e := range s.early {
		if now.Sub(e.at) > earlyMaxAge {
			delete(s.early, src)
		}
	}
	s.pruneExpiredOffersLocked(now)
}

func (s *Server) OurAddress() *net.UDPAddr {
	return s.ourSendAddr
}

// change notifies OnChange. Peers map versions are ignored and our own counter is used
// instead, so other changes (eg file transfers progress) also get distinct versions.
func (s *Server) change(_ uint64) {
	v := s.changes.Add(1)
	if s.OnChange != nil {
		s.OnChange(v)
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
			if bytes.HasPrefix(buf[:n], []byte(discoveryPrefix)) {
				s.handleDiscovery(buf[:n], addr) // discovery sent directly to us (see directDiscovery).
				continue
			}
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
			s.handleDiscovery(buf[:n], addr)
		}
	}
}

// handleDiscovery processes a discovery message, received by multicast or directly (unicast).
func (s *Server) handleDiscovery(buf []byte, addr *net.UDPAddr) {
	d, err := s.MCastMessageDecode(buf)
	if err != nil {
		log.Errf("Error decoding UDP packet %q from %v: %v", buf, addr, err)
		return
	}
	theirEpoch := d.Epoch
	data := PeerData{Port: addr.Port, Epoch: theirEpoch, LastSeen: time.Now(), RunID: d.RunID}
	peer := Peer{Name: d.Name, IP: addr.IP.String(), PublicKey: d.PublicKey}
	us := Peer{Name: s.Name, IP: s.ourSendAddr.IP.String(), PublicKey: s.idStr}
	if peer == us {
		if theirEpoch <= s.epoch.Load() {
			log.FErrf("Duplicate newer name,ip,pubkey detected... exiting (%q run %q epoch %d)", peer, data.RunID, data.Epoch)
			s.Stop()
		} else {
			log.Warnf("Duplicate older name,ip,pubkey detected... ignoring - they should exit (%q run %q epoch %d)",
				peer, data.RunID, data.Epoch)
		}
		return
	}
	var early []byte
	defer func() { // runs after the unlock below.
		if early != nil {
			s.handleDirectMessage(early, addr)
		}
	}()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if v, ok := s.Peers.Get(peer); ok {
		log.S(log.Verbose, "Already known peer", log.Any("Peer", peer), log.Any("OldData", v), log.Any("NewData", data))
		// Transfer the human hash (same pub key so same human hash)
		data.HumanHash = v.HumanHash
		// as well as the status
		data.Status = v.Status
		data.Clip = v.Clip
		data.ClipTime = v.ClipTime
		// Check if this is an updated port or a restarted peer.
		if v.Port != data.Port || v.RunID != data.RunID {
			log.Infof("Peer %q restarted or port changed: port %d -> %d, run %q -> %q",
				peer, v.Port, data.Port, v.RunID, data.RunID)
			data.Status = NotLinked
			src := Source{IP: peer.IP, Port: v.Port} // old source to delete
			s.Sources.Delete(src)
			src.Port = data.Port
			s.Sources.Set(src, peer)
			s.dropLinkLocked(peer)
			delete(s.lastAuto, peer)
			s.expireOffersLocked(peer, "peer restarted")
			s.sendDiscoveryTo(addr) // see below for new peers.
			early = s.takeEarlyLocked(src)
		}
		// Update last seen and epoch
		s.change(s.Peers.Set(peer, data))
		if early == nil { // otherwise its connect request is handled instead.
			s.autoConnectLocked(peer, data, time.Now())
		}
		return
	}
	pub, err := tcrypto.IdentityPublicKeyString(peer.PublicKey)
	data.HumanHash = tcrypto.HumanHash(pub)
	if err != nil {
		log.Errf("Failed to decode peer %q public key %q: %v", peer.Name, peer.PublicKey, err)
		data.HumanHash = "BAD-PKEY"
	}
	nv := s.Peers.Set(peer, data)
	src := Source{IP: peer.IP, Port: data.Port}
	s.Sources.Set(src, peer)
	log.S(log.Info, "New peer", log.Any("count", s.Peers.Len()),
		log.Any("Peer", peer), log.Any("Data", data))
	if !data.Compatible() {
		log.Warnf("%s (%s) runs an older, incompatible tsync version: upgrade it to connect", peer.Name, peer.IP)
	}
	s.change(nv)
	// Tell the new peer about us right away (it may have just started and not seen our
	// multicast yet) so a connect request from either side isn't from an unknown source.
	s.sendDiscoveryTo(addr)
	if early = s.takeEarlyLocked(src); early == nil {
		s.autoConnectLocked(peer, data, time.Now())
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
	DiscoveryMessageFormat = "tsync1 %q %s e %d r %s" // name, public key, epoch, run id
	ConnectMessageFormat   = "connect1 %q %q %d %s"   // from_name, target_name, tcp link port, signed ephemeral key
	AcceptMessageFormat    = "accept1 %q %q %d %s"    // from_name, target_name, tcp link port, signed ephemeral key
	DialMeMessageFormat    = "dialme1 %q %q"          // from_name, target_name: our dial failed, please dial us.
	RejectMessageFormat    = "reject1 %q %q %q"       // from_name, target_name, reason
)

func newRunID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) discoveryPayload(epoch int32) []byte {
	return []byte(fmt.Sprintf(DiscoveryMessageFormat, s.Name, s.idStr, epoch, s.runID))
}

// sendDiscoveryTo sends our discovery message directly to addr.
func (s *Server) sendDiscoveryTo(addr *net.UDPAddr) {
	if _, err := s.dualUDPSock.WriteToUDP(s.discoveryPayload(s.epoch.Load()), addr); err != nil {
		log.LogVf("Error sending direct discovery to %v: %v", addr, err)
	}
}

// discoveryPrefix is the start of DiscoveryMessageFormat.
const discoveryPrefix = "tsync1 "

// directDiscovery also sends our discovery message directly (unicast) to the configured
// static peers and to the peers we know: when multicast only works one way (eg a NAT'ed
// VM sees the host but not the other way around) or not at all, they still discover us.
func (s *Server) directDiscovery(epoch int32) {
	payload := s.discoveryPayload(epoch)
	targets := slices.Clone(s.staticPeers)
	for peer, data := range s.Peers.All() {
		targets = append(targets, &net.UDPAddr{IP: net.ParseIP(peer.IP), Port: data.Port})
	}
	for _, a := range targets {
		if _, err := s.dualUDPSock.WriteToUDP(payload, a); err != nil {
			log.LogVf("Error sending direct discovery to %v: %v", a, err)
		}
	}
}

func (s *Server) MCastMessageSend(epoch int32) error {
	payload := fmt.Sprintf(DiscoveryMessageFormat, s.Name, s.idStr, epoch, s.runID)
	_, err := s.dualUDPSock.WriteToUDP([]byte(payload), s.destAddr)
	return err
}

// Discovery is a decoded discovery message.
type Discovery struct {
	Name      string
	PublicKey string
	Epoch     int32
	RunID     string // empty for older versions.
}

func (s *Server) MCastMessageDecode(buf []byte) (Discovery, error) {
	var d Discovery
	n, err := fmt.Sscanf(string(buf), DiscoveryMessageFormat, &d.Name, &d.PublicKey, &d.Epoch, &d.RunID)
	if n < 3 { // older versions don't have the run id.
		if err == nil {
			err = fmt.Errorf("could not decode message %q", string(buf))
		}
		return d, err
	}
	if err = ValidateName(d.Name); err != nil {
		return d, err
	}
	return d, nil
}

// MaxNameLen is the maximum length in bytes of a peer name.
const MaxNameLen = 64

// ValidateName checks that a peer name is non empty, at most MaxNameLen bytes of valid
// UTF-8 without control characters (so it's safe to display, eg in a terminal).
func ValidateName(name string) error {
	if name == "" || len(name) > MaxNameLen {
		return fmt.Errorf("invalid name length %d (must be 1-%d bytes)", len(name), MaxNameLen)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("invalid name %q: not valid UTF-8", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid name %q: contains control characters", name)
		}
	}
	return nil
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
