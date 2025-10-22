package tsnet_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
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

	// Wait a bit for the connection message to be received
	time.Sleep(200 * time.Millisecond)

	// Check that the connection was created on A's side
	connA, exists := serverA.Peers.Get(peerB)
	if !exists || connA.Status != tsnet.SentConn {
		t.Fatal("Connection from A to B not found in A's connection map")
	}
	t.Logf("✓ Connection created on A's side: status %v", connA.Status)

	t.Log("✓ Test completed successfully!")
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
