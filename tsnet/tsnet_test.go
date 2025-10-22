package tsnet_test

import (
	"context"
	"fmt"
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

func TestPeerDiscovery(t *testing.T) {
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
	time.Sleep(1400 * time.Millisecond)

	// Check that Host A discovered Host B
	var peerB tsnet.Peer
	foundB := false
	for peer := range serverA.Peers.All() {
		if peer.Name == "HostB" {
			peerB = peer
			foundB = true
			t.Logf("HostA discovered HostB: %v", peer)
			break
		}
	}
	if !foundB {
		t.Fatalf("HostA did not discover HostB")
	}

	// Check that Host B discovered Host A
	var peerA tsnet.Peer
	foundA := false
	for peer := range serverB.Peers.All() {
		if peer.Name == "HostA" {
			peerA = peer
			foundA = true
			t.Logf("HostB discovered HostA: %v", peer)
			break
		}
	}
	if !foundA {
		t.Fatalf("HostB did not discover HostA")
	}

	t.Logf("Peer discovery successful! Both hosts discovered each other. %v <-> %v", peerB, peerA)
}

func TestMultiplePeersDiscovery(t *testing.T) {
	log.SetLogLevel(log.Info)

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
		t.Logf("Started Host%d on port %d", i, 29570+i)
	}

	// Wait for discovery
	t.Log("Waiting for peer discovery...")
	time.Sleep(2000 * time.Millisecond)

	// Each peer should discover all other peers
	for i, srv := range servers {
		peerCount := srv.Peers.Len()
		expected := numPeers - 1 // All others except itself
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
