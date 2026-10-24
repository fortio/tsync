package tsnet

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fortio.org/log"
	"fortio.org/tsync/tcrypto"
)

func TestFrames(t *testing.T) {
	idA, _ := tcrypto.NewIdentity()
	idB, _ := tcrypto.NewIdentity()
	ephA, _ := tcrypto.NewEphemeralKeys()
	ephB, _ := tcrypto.NewEphemeralKeys()
	const ctx = "test"
	sessA, err := idA.VerifiedSession(ephA, idB.SignEphemeral(ephB, ctx), ctx, idB.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := idB.VerifiedSession(ephB, idA.SignEphemeral(ephA, ctx), ctx, idA.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err = writeFrame(&buf, sessA, FrameHello, nil); err != nil {
		t.Fatal(err)
	}
	if err = writeFrame(&buf, sessA, FrameClip, []byte("clip")); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"H", string(FrameClip) + "clip"} {
		frame, rerr := readFrame(&buf)
		if rerr != nil {
			t.Fatal(rerr)
		}
		plain, oerr := sessB.Open(frame)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if string(plain) != expected {
			t.Errorf("got %q, expected %q", plain, expected)
		}
	}
	// Oversized frame header is rejected before allocating.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], maxFrameSize+1)
	if _, err = readFrame(bytes.NewReader(hdr[:])); err == nil {
		t.Errorf("expected error for oversized frame")
	}
	// Truncated frame.
	binary.BigEndian.PutUint32(hdr[:], 10)
	if _, err = readFrame(bytes.NewReader(append(hdr[:], 1, 2, 3))); err == nil {
		t.Errorf("expected error for truncated frame")
	}
}

func TestValidateNameAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"host1", true},
		{"My Laptop (é ✂️)", true},
		{"", false},
		{"esc\x1b[31m", false},
		{"nl\nx", false},
		{"c1\u009b", false},
		{"bad\xffutf8", false},
		{string(make([]byte, MaxNameLen+1)), false},
	} {
		err := ValidateName(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateName(%q) = %v, expected ok %t", tc.name, err, tc.ok)
		}
	}
	s := &Server{}
	d, err := s.MCastMessageDecode([]byte(`tsync1 "ok" p.key e 3 r abc`))
	if err != nil || d != (Discovery{Name: "ok", PublicKey: "p.key", Epoch: 3, RunID: "abc"}) {
		t.Errorf("valid discovery message: %+v %v", d, err)
	}
	if d, err = s.MCastMessageDecode([]byte(`tsync1 "old" p.key e 3`)); err != nil || d.RunID != "" || d.Epoch != 3 {
		t.Errorf("older version discovery message: %+v %v", d, err)
	}
	if (PeerData{}).Compatible() || !(PeerData{RunID: "x"}).Compatible() {
		t.Errorf("Compatible() wrong")
	}
	if _, err = s.MCastMessageDecode([]byte(`tsync1 "x" p.key`)); err == nil {
		t.Errorf("truncated discovery message accepted")
	}
	if _, err := s.MCastMessageDecode([]byte(`tsync1 "evil\x1b]52;c;aGk=\a" p.key e 3 r x`)); err == nil {
		t.Errorf("discovery message with escape in name accepted")
	}
}

func TestSafeFileName(t *testing.T) {
	for _, tc := range []struct{ in, out string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\x\evil.exe`, "evil.exe"},
		{`C:\World of Warcraft\_classic_beta_\WTF\Config.wtf`, "Config.wtf"},
		{"..", "file"},
		{"", "file"},
		{".bashrc", "_.bashrc"},
		{"a<b>c:d|e?f*g\"h.txt", "a_b_c_d_e_f_g_h.txt"},
		{"ctrl\x1b[31m\nname.txt", "ctrl_[31m_name.txt"},
		{"CON", "_CON"},
		{"com1.txt", "_com1.txt"},
		{"COM10.txt", "COM10.txt"},
		{"trailing dots... ", "trailing dots"},
		{"日本語 ✂️.md", "日本語 ✂️.md"},
		{"bad\xffutf8", "bad_utf8"},
		{"Screenshot 2026-09-17 at 12.25.39\u202fPM.png", "Screenshot 2026-09-17 at 12.25.39 PM.png"},
		{"no\u00a0break\u2009thin.txt", "no break thin.txt"},
		{"invoice\u202efdp.exe", "invoicefdp.exe"}, // RLO would display as invoiceexe.pdf.
		{"zero\u200bwidth\ufeff\u00adx\u2060.txt", "zerowidthx.txt"},
		{"famille 👨\u200d👩\u200d👧.jpg", "famille 👨\u200d👩\u200d👧.jpg"},
		{"Café crème 日本語\u3000メモ ✂️.md", "Café crème 日本語\u3000メモ ✂️.md"},
	} {
		if got := SafeFileName(tc.in); got != tc.out {
			t.Errorf("SafeFileName(%q) = %q, expected %q", tc.in, got, tc.out)
		}
	}
	long := SafeFileName(strings.Repeat("é", 300) + ".txt")
	if len(long) > MaxFileNameLen || !strings.HasSuffix(long, ".txt") || !utf8.ValidString(long) {
		t.Errorf("long name not truncated properly: %d %q", len(long), long)
	}
}

func TestReserveName(t *testing.T) {
	dir := t.TempDir()
	for _, expected := range []string{"a.txt", "a-1.txt", "a-2.txt"} {
		p, err := reserveName(dir, "a.txt")
		if err != nil || filepath.Base(p) != expected {
			t.Fatalf("reserveName got %q %v, expected %q", p, err, expected)
		}
	}
	if p, err := reserveName(dir, "noext"); err != nil || filepath.Base(p) != "noext" {
		t.Errorf("unexpected %q %v", p, err)
	}
	if p, err := reserveName(dir, "noext"); err != nil || filepath.Base(p) != "noext-1" {
		t.Errorf("unexpected %q %v", p, err)
	}
}

// Normal dial fails (as if the peer was behind NAT): the peer must be asked to dial us instead.
func TestDialMeFallback(t *testing.T) {
	if os.Getenv("CI") != "" && runtime.GOOS == "darwin" {
		t.Skip("Skipping multicast test on macOS in CI (no multicast support)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	servers := make([]*Server, 0, 2)
	for _, name := range []string{"NatA", "NatB"} {
		id, _ := tcrypto.NewIdentity()
		cfg := Config{
			Name: name, Port: DefaultDiscoveryPort + 13, Mcast: "239.255.115.123", Target: DefaultTarget,
			Identity: id, BaseBroadcastInterval: 100 * time.Millisecond,
		}
		srv := cfg.NewServer()
		srv.testDialAddr = func(addr string, fallback bool) string {
			if fallback {
				return addr
			}
			return "127.0.0.1:1" // nothing listens there.
		}
		if err := srv.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer srv.Stop()
		servers = append(servers, srv)
	}
	find := func(srv *Server, name string) Peer {
		for {
			for p := range srv.Peers.All() {
				if p.Name == name {
					return p
				}
			}
			if ctx.Err() != nil {
				t.Fatalf("%s didn't discover %s", srv.Name, name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	peerB := find(servers[0], "NatB")
	peerA := find(servers[1], "NatA")
	if err := servers[0].ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	for {
		if d, _ := servers[1].Peers.Get(peerA); d.Status == ReceivedConn {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("B didn't get the connection request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := servers[1].ConnectToPeer(peerA); err != nil {
		t.Fatal(err)
	}
	for !servers[0].Linked(peerB) || !servers[1].Linked(peerA) {
		if ctx.Err() != nil {
			t.Fatal("link not established through the dial me fallback")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// handleDiscovery paths without network (messages fed directly).
func TestHandleDiscovery(t *testing.T) {
	id, _ := tcrypto.NewIdentity()
	other, _ := tcrypto.NewIdentity()
	s := (&Config{Name: "Us", Identity: id, NoAutoConnect: true}).NewServer()
	s.idStr = id.PublicKeyToString()
	s.runID = "ourrun"
	s.ourSendAddr = &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1000}
	var err error
	if s.dualUDPSock, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	defer s.dualUDPSock.Close()
	them, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer them.Close()
	from := them.LocalAddr().(*net.UDPAddr)
	key := other.PublicKeyToString()
	msg := func(name, k string, epoch int, run string) []byte {
		return []byte(fmt.Sprintf(DiscoveryMessageFormat, name, k, epoch, run))
	}
	get := func(name string) (Peer, PeerData) {
		t.Helper()
		for p, d := range s.Peers.All() {
			if p.Name == name {
				return p, d
			}
		}
		t.Fatalf("peer %s not found", name)
		return Peer{}, PeerData{}
	}
	s.handleDiscovery([]byte("garbage"), from) // decode error: ignored.
	s.handleDiscovery(msg("Them", key, 1, "run1"), from)
	peer, data := get("Them")
	// New peer gets our discovery message right away.
	_ = them.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, BufSize)
	n, _, err := them.ReadFromUDP(buf)
	if err != nil || !bytes.HasPrefix(buf[:n], []byte(discoveryPrefix+`"Us"`)) {
		t.Errorf("expected our discovery sent to new peer, got %q, %v", buf[:n], err)
	}
	if data.RunID != "run1" || data.Port != from.Port || data.HumanHash != other.HumanID() || !data.Compatible() {
		t.Errorf("unexpected new peer data %+v", data)
	}
	// Pretend we're connected, then it restarts (new run id): reset to NotLinked.
	s.stateMu.Lock()
	data.Status = Connected
	s.Peers.Set(peer, data)
	s.links[peer] = &link{}
	s.stateMu.Unlock()
	s.handleDiscovery(msg("Them", key, 1, "run2"), from)
	_, data = get("Them")
	if data.Status != NotLinked || data.RunID != "run2" || s.links[peer] != nil {
		t.Errorf("restart not detected: %+v", data)
	}
	// Port change also resets and updates the source map.
	from2 := &net.UDPAddr{IP: from.IP, Port: from.Port + 1}
	s.handleDiscovery(msg("Them", key, 2, "run2"), from2)
	if p, ok := s.Sources.Get(Source{IP: "127.0.0.1", Port: from2.Port}); !ok || p != peer {
		t.Errorf("source map not updated")
	}
	if _, ok := s.Sources.Get(Source{IP: "127.0.0.1", Port: from.Port}); ok {
		t.Errorf("old source not removed")
	}
	// Older version (no run id): flagged, can't connect.
	s.handleDiscovery([]byte(fmt.Sprintf(`tsync1 %q %s e %d`, "Old", key, 1)), from)
	oldPeer, oldData := get("Old")
	if oldData.Compatible() {
		t.Errorf("old version peer should not be compatible")
	}
	if err := s.ConnectToPeer(oldPeer); err == nil || !strings.Contains(err.Error(), "older") {
		t.Errorf("expected incompatible version error, got %v", err)
	}
	// Bad public key.
	s.handleDiscovery(msg("BadKey", "p.notakey", 1, "r"), from)
	if _, d := get("BadKey"); d.HumanHash != "BAD-PKEY" {
		t.Errorf("expected BAD-PKEY, got %q", d.HumanHash)
	}
	// Older duplicate of ourselves (same name, ip, key): ignored, newer one: we stop.
	s.epoch.Store(5)
	s.handleDiscovery(msg("Us", s.idStr, 9, "x"), s.ourSendAddr)
	if s.Stopped() {
		t.Errorf("should not stop for an older duplicate")
	}
	s.handleDiscovery(msg("Us", s.idStr, 3, "x"), s.ourSendAddr)
	if !s.Stopped() {
		t.Errorf("should stop when a newer duplicate is detected")
	}
	// Dial me requests from unknown source or not connected peer are ignored.
	s.handleDialMe(&net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1}, "x", "Us")
	s.handleDialMe(from2, "Them", "Us")
}

func TestPeersCleanupNotifiesChange(t *testing.T) {
	var notified uint64
	s := (&Config{PeerTimeout: time.Second, OnChange: func(v uint64) { notified = v }}).NewServer()
	peer := Peer{Name: "gone", IP: "10.0.0.1", PublicKey: "k"}
	s.Peers.Set(peer, PeerData{Port: 1234, LastSeen: time.Now().Add(-2 * time.Second)})
	s.Sources.Set(Source{IP: peer.IP, Port: 1234}, peer)
	offered := &Transfer{Peer: peer, Incoming: true, State: TransferOffered}
	s.addTransferLocked(offered)
	s.PeersCleanup()
	if s.Peers.Len() != 0 || s.Sources.Len() != 0 {
		t.Errorf("expired peer not removed: %d peers, %d sources", s.Peers.Len(), s.Sources.Len())
	}
	if notified == 0 {
		t.Errorf("OnChange not called after removing the last peer")
	}
	if offered.State != TransferFailed || len(s.transfers) != 1 {
		t.Errorf("offer from expired peer should be failed (and still listed), got %v", offered.State)
	}
	offered.End = offered.End.Add(-2 * expiredOfferLinger)
	s.PeersCleanup()
	if len(s.transfers) != 0 {
		t.Errorf("expired offer should be removed after a bit")
	}
}

// A connect request received before the sender's discovery is handled once it's discovered.
func TestEarlyConnect(t *testing.T) {
	id, _ := tcrypto.NewIdentity()
	other, _ := tcrypto.NewIdentity()
	s := (&Config{Name: "Us", Identity: id, NoAutoConnect: true, PeerTimeout: time.Minute}).NewServer()
	s.idStr = id.PublicKeyToString()
	s.runID = "ourrun"
	s.ourSendAddr = &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1000}
	var err error
	if s.dualUDPSock, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	defer s.dualUDPSock.Close()
	from := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9} // discard port, nobody listens.
	eph, _ := tcrypto.NewEphemeralKeys()
	signed := other.SignEphemeral(eph, s.linkContext(Peer{Name: "Them"}, PeerData{RunID: "r1"}))
	s.handleDirectMessage([]byte(fmt.Sprintf(ConnectMessageFormat, "Them", "Us", 1234, signed)), from)
	if len(s.early) != 1 || s.Peers.Len() != 0 {
		t.Fatalf("connect from unknown source should be stashed: %d early, %d peers", len(s.early), s.Peers.Len())
	}
	s.handleDiscovery([]byte(fmt.Sprintf(DiscoveryMessageFormat, "Them", other.PublicKeyToString(), 1, "r1")), from)
	if len(s.early) != 0 {
		t.Errorf("early message not consumed")
	}
	for p, d := range s.Peers.All() {
		if p.Name != "Them" || d.Status != ReceivedConn {
			t.Errorf("expected pending connection request from Them, got %v %+v", p, d)
		}
	}
	// Stale ones are dropped.
	s.handleDirectMessage([]byte("whatever"), &net.UDPAddr{IP: from.IP, Port: 10})
	for src, e := range s.early {
		e.at = e.at.Add(-2 * earlyMaxAge)
		s.early[src] = e
	}
	s.PeersCleanup()
	if len(s.early) != 0 {
		t.Errorf("stale early message not dropped")
	}
}

// Network provided strings (unauthenticated discovery and direct messages) are quoted in the
// (text, terminal) logs so they can't inject escape sequences.
func TestNoEscapesInLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Config
	log.Config.JSON = false
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.Config = prev
		log.SetOutput(os.Stderr)
	})
	s := (&Config{Name: "us", PeerTimeout: time.Second}).NewServer()
	d, err := s.MCastMessageDecode([]byte("tsync1 \"x\" p.\x1b]52;c;aGk=\a e 1 r \x1b[2J"))
	if err != nil {
		t.Fatal(err)
	}
	peer := Peer{Name: d.Name, IP: "10.0.0.1", PublicKey: d.PublicKey}
	s.Peers.Set(peer, PeerData{Port: 1234, LastSeen: time.Now(), RunID: d.RunID})
	s.Sources.Set(Source{IP: peer.IP, Port: 1234}, peer)
	from := &net.UDPAddr{IP: net.ParseIP(peer.IP), Port: 1234}
	s.handleDirectMessage([]byte(`connect1 "\x1b[31mevil" "us" 1 s.x`), from)
	s.handleDirectMessage([]byte(`accept1 "x" "\x1b[31mus" 1 s.x`), from)
	s.handleConnect(from, "x", "us", 1, "s.x") // invalid public key.
	// Reject (not signed) from a peer with a valid key.
	id, _ := tcrypto.NewIdentity()
	good := Peer{Name: "good", IP: "10.0.0.2", PublicKey: id.PublicKeyToString()}
	s.Peers.Set(good, PeerData{Port: 1235, LastSeen: time.Now()})
	s.Sources.Set(Source{IP: good.IP, Port: 1235}, good)
	s.handleDirectMessage([]byte(`reject1 "good" "us" "\x1b]0;title\a"`), &net.UDPAddr{IP: net.ParseIP(good.IP), Port: 1235})
	s.Peers.Set(peer, PeerData{Port: 1234, LastSeen: time.Now().Add(-2 * time.Second), RunID: d.RunID})
	s.PeersCleanup()
	out := buf.String()
	if !strings.Contains(out, "Removing 1 expired peers") || !strings.Contains(out, "rejected by good") {
		t.Errorf("expected log lines missing: %s", out)
	}
	if strings.ContainsAny(out, "\x1b\a") {
		t.Errorf("escape sequences in the logs: %q", out)
	}
}
