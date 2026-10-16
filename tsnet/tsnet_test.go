package tsnet_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

const (
	testMultiCastAddr = "239.255.115.116"              // swapped last two octets to avoid conflict with default
	testPort          = tsnet.DefaultDiscoveryPort + 1 // Use different port than default to avoid conflicts
)

func NoMCastOnMacInCI(t *testing.T) {
	if os.Getenv("CI") != "" && runtime.GOOS == "darwin" {
		t.Skip("Skipping multicast test on macOS in CI (no multicast support)")
	}
}

func TestPeerDiscovery(t *testing.T) {
	NoMCastOnMacInCI(t)

	log.SetLogLevel(log.Info) // Set to Debug for more verbose output during test debugging

	// Create identities for both hosts
	identityA, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatalf("Failed to create identity A: %v", err)
	}
	identityB, err2 := tcrypto.NewIdentity()
	if err2 != nil {
		t.Fatalf("Failed to create identity B: %v", err2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfgA := tsnet.Config{
		Name:                  "HostA",
		Port:                  testPort,
		Mcast:                 testMultiCastAddr,
		Target:                tsnet.DefaultTarget,
		Identity:              identityA,
		BaseBroadcastInterval: 100 * time.Millisecond, // Faster for testing (jitter is still up to 1 second)
	}

	// Configure Host B
	cfgB := tsnet.Config{
		Name:                  "HostB",
		Port:                  testPort, // Same port for multicast group
		Mcast:                 testMultiCastAddr,
		Target:                tsnet.DefaultTarget,
		Identity:              identityB,
		BaseBroadcastInterval: cfgA.BaseBroadcastInterval, // Same interval
	}

	// Start Host A
	serverA := cfgA.NewServer()
	if startErr := serverA.Start(ctx); startErr != nil {
		t.Fatalf("Failed to start server A: %v", startErr)
	}
	defer serverA.Stop()
	t.Logf("Started HostA on port %d", cfgA.Port)

	// Start Host B
	serverB := cfgB.NewServer()
	if startErr := serverB.Start(ctx); startErr != nil {
		t.Fatalf("Failed to start server B: %v", startErr)
	}
	defer serverB.Stop()
	t.Logf("Started HostB on port %d", cfgB.Port)

	// Wait for peer discovery (should happen within a few broadcast intervals)
	t.Log("Waiting for peer discovery...")
	var peerB tsnet.Peer
	var peerA tsnet.Peer
	foundB := false
	foundA := false

	// Poll for discovery with timeout from context
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for !foundB || !foundA {
		select {
		case <-ctx.Done():
			if !foundB {
				t.Fatalf("HostA did not discover HostB within timeout")
			}
			if !foundA {
				t.Fatalf("HostB did not discover HostA within timeout")
			}
		case <-ticker.C:
			if !foundB {
				for peer := range serverA.Peers.All() {
					if peer.Name == "HostB" {
						peerB = peer
						foundB = true
						t.Logf("HostA discovered HostB: %v", peer)
						break
					}
				}
			}
			if !foundA {
				for peer := range serverB.Peers.All() {
					if peer.Name == "HostA" {
						peerA = peer
						foundA = true
						t.Logf("HostB discovered HostA: %v", peer)
						break
					}
				}
			}
		}
	}

	t.Logf("Peer discovery successful! Both hosts discovered each other. %v <-> %v", peerB, peerA)

	// Now test direct connection from A to B
	t.Log("Testing direct connection from HostA to HostB...")
	err = serverA.ConnectToPeer(peerB)
	if err != nil {
		t.Fatalf("Failed to initiate connection from A to B: %v", err)
	}

	waitForStatus(ctx, t, serverA, peerB, tsnet.SentConn)
	waitForStatus(ctx, t, serverB, peerA, tsnet.ReceivedConn)

	// B accepts (user clicking on A in B's UI).
	if err = serverB.ConnectToPeer(peerA); err != nil {
		t.Fatalf("Failed to accept connection from A on B: %v", err)
	}
	waitForStatus(ctx, t, serverB, peerA, tsnet.Connected)
	waitForStatus(ctx, t, serverA, peerB, tsnet.Connected)
	checkClips(ctx, t, serverA, peerA, serverB, peerB)
	t.Log("✓ Test completed successfully!")
}

// checkClips waits for the TCP link between A and B and checks clipboard content goes both ways.
func checkClips(ctx context.Context, t *testing.T,
	serverA *tsnet.Server, peerA tsnet.Peer, serverB *tsnet.Server, peerB tsnet.Peer,
) {
	t.Helper()
	waitFor(ctx, t, "A linked to B", func() bool { return serverA.Linked(peerB) })
	waitFor(ctx, t, "B linked to A", func() bool { return serverB.Linked(peerA) })
	sendClip(ctx, t, serverA, serverB, peerA, "hello\nfrom A ✂️")
	sendClip(ctx, t, serverB, serverA, peerB, "and back from B")
	sendClip(ctx, t, serverA, serverB, peerA, "") // empty clip is valid too.
}

func sendClip(ctx context.Context, t *testing.T, from, to *tsnet.Server, fromPeer tsnet.Peer, clip string) {
	t.Helper()
	n, err := from.SendClip(clip)
	if err != nil || n != 1 {
		t.Fatalf("%s SendClip: sent to %d peers, err %v", from.Name, n, err)
	}
	waitFor(ctx, t, to.Name+" received clip from "+from.Name, func() bool {
		data, ok := to.Peers.Get(fromPeer)
		return ok && data.Clip == clip && !data.ClipTime.IsZero()
	})
}

func waitFor(ctx context.Context, t *testing.T, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Logf("✓ %s", what)
}

// waitForStatus waits for srv to see peer in the given status.
func waitForStatus(ctx context.Context, t *testing.T, srv *tsnet.Server, peer tsnet.Peer, status tsnet.ConnectionStatus) {
	t.Helper()
	for {
		data, ok := srv.Peers.Get(peer)
		if ok && data.Status == status {
			t.Logf("✓ %s sees %s as %v", srv.Name, peer.Name, status)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: timeout waiting for %s to be %v (is %v, found %t)", srv.Name, peer.Name, status, data.Status, ok)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// waitForPeer waits for srv to discover a peer with the given name.
func waitForPeer(ctx context.Context, t *testing.T, srv *tsnet.Server, name string) tsnet.Peer {
	t.Helper()
	for {
		for peer := range srv.Peers.All() {
			if peer.Name == name {
				return peer
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: timeout waiting to discover %s", srv.Name, name)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func startTestServer(ctx context.Context, t *testing.T, name string, id *tcrypto.Identity, storage *tcrypto.Storage) *tsnet.Server {
	t.Helper()
	cfg := tsnet.Config{
		Name:                  name,
		Port:                  testPort,
		Mcast:                 testMultiCastAddr,
		Target:                tsnet.DefaultTarget,
		Identity:              id,
		BaseBroadcastInterval: 100 * time.Millisecond,
		Storage:               storage,
	}
	srv := cfg.NewServer()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Failed to start %s: %v", name, err)
	}
	return srv
}

func TestHandshakeTrustedAndSimultaneous(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idA, _ := tcrypto.NewIdentity()
	idB, _ := tcrypto.NewIdentity()
	storageA := &tcrypto.Storage{Dir: t.TempDir()}
	storageB := &tcrypto.Storage{Dir: t.TempDir()}
	// First session: simultaneous connect (both users click) -> connected without further accept.
	serverA := startTestServer(ctx, t, "HostA", idA, storageA)
	serverB := startTestServer(ctx, t, "HostB", idB, storageB)
	peerB := waitForPeer(ctx, t, serverA, "HostB")
	peerA := waitForPeer(ctx, t, serverB, "HostA")
	if err := serverA.ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	if err := serverB.ConnectToPeer(peerA); err != nil {
		t.Fatal(err)
	}
	waitForStatus(ctx, t, serverA, peerB, tsnet.Connected)
	waitForStatus(ctx, t, serverB, peerA, tsnet.Connected)
	checkClips(ctx, t, serverA, peerA, serverB, peerB)
	serverA.Stop()
	serverB.Stop()
	for _, st := range []*tcrypto.Storage{storageA, storageB} {
		trusted, err := st.LoadTrusted()
		if err != nil || len(trusted) != 1 {
			t.Fatalf("expected 1 trusted peer in %s, got %v %v", st.Dir, trusted, err)
		}
	}
	// Restart both (same identities and storage): they auto connect to each other (trusted).
	serverA = startTestServer(ctx, t, "HostA", idA, storageA)
	defer serverA.Stop()
	serverB = startTestServer(ctx, t, "HostB", idB, storageB)
	defer serverB.Stop()
	peerB = waitForPeer(ctx, t, serverA, "HostB")
	peerA = waitForPeer(ctx, t, serverB, "HostA")
	waitForStatus(ctx, t, serverA, peerB, tsnet.Connected)
	waitForStatus(ctx, t, serverB, peerA, tsnet.Connected)
	checkClips(ctx, t, serverA, peerA, serverB, peerB)
	// B going away: A's link drops (and A then tries to auto reconnect).
	serverB.Stop()
	waitFor(ctx, t, "A no longer connected to stopped B", func() bool {
		data, _ := serverA.Peers.Get(peerB)
		return data.Status != tsnet.Connected && !serverA.Linked(peerB)
	})
}

func TestRetryAndLastClipForNewLink(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idA, _ := tcrypto.NewIdentity()
	idB, _ := tcrypto.NewIdentity()
	// A broadcasts slowly so B discovers it late: A's first connect request (as soon as it
	// sees B) gets dropped by B as from an unknown peer, and must be resent.
	cfgA := tsnet.Config{
		Name: "HostA", Port: testPort, Mcast: testMultiCastAddr, Target: tsnet.DefaultTarget,
		Identity: idA, BaseBroadcastInterval: 1500 * time.Millisecond,
	}
	serverA := cfgA.NewServer()
	if err := serverA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer serverA.Stop()
	// Pasted before anyone is linked.
	n, err := serverA.SendClip("early clip")
	if err != nil || n != 0 {
		t.Fatalf("SendClip with no peers: %d %v", n, err)
	}
	sentTime := time.Now()
	early := filepath.Join(t.TempDir(), "early.txt")
	_ = os.WriteFile(early, []byte("x"), 0o644)
	if n, err = serverA.OfferFile(early); err != nil || n != 0 {
		t.Fatalf("OfferFile with no peers: %d %v", n, err)
	}
	serverB := startTestServer(ctx, t, "HostB", idB, nil)
	defer serverB.Stop()
	peerB := waitForPeer(ctx, t, serverA, "HostB")
	if err = serverA.ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	peerA := waitForPeer(ctx, t, serverB, "HostA")
	waitForStatus(ctx, t, serverB, peerA, tsnet.ReceivedConn)
	if err = serverB.ConnectToPeer(peerA); err != nil {
		t.Fatal(err)
	}
	var clipTime time.Time
	waitFor(ctx, t, "B got A's early clip once linked", func() bool {
		data, _ := serverB.Peers.Get(peerA)
		clipTime = data.ClipTime
		return data.Clip == "early clip"
	})
	waitTransfer(ctx, t, serverB, true, "early.txt", tsnet.TransferOffered) // file offer too.
	if clipTime.After(sentTime) {
		t.Errorf("clip time %v should be the original time (before %v)", clipTime, sentTime)
	}
	// Reconnecting resends it, but it's not newer so nothing changes.
	if err = serverA.ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	waitForStatus(ctx, t, serverA, peerB, tsnet.SentConn)
	waitFor(ctx, t, "A linked again", func() bool { return serverA.Linked(peerB) })
	waitFor(ctx, t, "B linked again", func() bool { return serverB.Linked(peerA) })
	data, _ := serverB.Peers.Get(peerA)
	if data.Clip != "early clip" || !data.ClipTime.Equal(clipTime) {
		t.Errorf("unexpected clip after resend: %q %v (vs %v)", data.Clip, data.ClipTime, clipTime)
	}
}

func TestClipToMultiplePeers(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// All on the same IP so incoming TCP links must be matched to the right session.
	names := []string{"HostA", "HostB", "HostC"}
	servers := make([]*tsnet.Server, len(names))
	for i, name := range names {
		id, _ := tcrypto.NewIdentity()
		servers[i] = startTestServer(ctx, t, name, id, nil)
		defer servers[i].Stop()
	}
	// A connects to B and C, which accept.
	for _, other := range servers[1:] {
		// Both must have discovered each other first, or the request is dropped as from an unknown source.
		peer := waitForPeer(ctx, t, servers[0], other.Name)
		peerA := waitForPeer(ctx, t, other, "HostA")
		if err := servers[0].ConnectToPeer(peer); err != nil {
			t.Fatal(err)
		}
		waitForStatus(ctx, t, other, peerA, tsnet.ReceivedConn)
		if err := other.ConnectToPeer(peerA); err != nil {
			t.Fatal(err)
		}
		waitFor(ctx, t, "A linked to "+other.Name, func() bool { return servers[0].Linked(peer) })
		waitFor(ctx, t, other.Name+" linked to A", func() bool { return other.Linked(peerA) })
	}
	clip := "to everyone"
	n, err := servers[0].SendClip(clip)
	if err != nil || n != 2 {
		t.Fatalf("SendClip: sent to %d peers, err %v", n, err)
	}
	for _, other := range servers[1:] {
		peerA := waitForPeer(ctx, t, other, "HostA")
		waitFor(ctx, t, other.Name+" got clip", func() bool {
			data, _ := other.Peers.Get(peerA)
			return data.Clip == clip
		})
	}
	// B and C aren't connected to each other.
	n, err = servers[1].SendClip("only to A")
	if err != nil || n != 1 {
		t.Fatalf("B SendClip: sent to %d peers, err %v", n, err)
	}
	if _, err = servers[0].SendClip(strings.Repeat("x", tsnet.MaxClipSize+1)); err == nil {
		t.Errorf("expected error for too large clip")
	}
}

func TestMultiplePeersDiscovery(t *testing.T) {
	NoMCastOnMacInCI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	numPeers := 3
	servers := make([]*tsnet.Server, numPeers)
	identities := make([]*tcrypto.Identity, numPeers)

	// Create and start multiple peers
	for i := range numPeers {
		id, err := tcrypto.NewIdentity()
		if err != nil {
			t.Fatalf("Failed to create identity %d: %v", i, err)
		}
		identities[i] = id

		cfg := tsnet.Config{
			Name:                  fmt.Sprintf("Host%d", i),
			Port:                  testPort,
			Mcast:                 testMultiCastAddr,
			Target:                tsnet.DefaultTarget,
			Identity:              id,
			BaseBroadcastInterval: 100 * time.Millisecond,
		}

		servers[i] = cfg.NewServer()
		if err := servers[i].Start(ctx); err != nil {
			t.Fatalf("Failed to start server %d: %v", i, err)
		}
		defer servers[i].Stop()
		t.Logf("Started Host%d on port %d", i, servers[i].OurAddress().Port)
	}

	// Wait for discovery
	t.Log("Waiting for peer discovery...")

	// Poll until all peers discover each other
	expected := numPeers - 1 // All others except itself
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	allDiscovered := false
	for !allDiscovered {
		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for all peers to discover each other")
		case <-ticker.C:
			allDiscovered = true
			for i, srv := range servers {
				if srv.Peers.Len() != expected {
					allDiscovered = false
					t.Logf("Host%d has %d/%d peers", i, srv.Peers.Len(), expected)
					break
				}
			}
		}
	}

	// Each peer should discover all other peers
	for i, srv := range servers {
		peerCount := srv.Peers.Len()
		if peerCount != expected {
			t.Errorf("Host%d discovered %d peers, expected %d", i, peerCount, expected)
			for peer := range srv.Peers.All() {
				t.Logf("  Host%d sees: %s", i, peer.Name)
			}
		} else {
			t.Logf("✓ Host%d correctly discovered %d peers", i, peerCount)
		}
	}
}
