package tsnet_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fortio.org/log"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

// headlessPair starts HostA (with the UI behavior) and a Headless HostB; trusted says if they already trust each other.
func headlessPair(ctx context.Context, t *testing.T, trusted bool, dirA, dirB string) (srvA, srvB *tsnet.Server) {
	t.Helper()
	idA, _ := tcrypto.NewIdentity()
	idB, _ := tcrypto.NewIdentity()
	storeA, storeB := &tcrypto.Storage{Dir: t.TempDir()}, &tcrypto.Storage{Dir: t.TempDir()}
	if trusted {
		if err := storeA.AddTrusted(idB.PublicKeyToString(), "HostB"); err != nil {
			t.Fatal(err)
		}
		if err := storeB.AddTrusted(idA.PublicKeyToString(), "HostA"); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string, id *tcrypto.Identity, st *tcrypto.Storage, dir string, headless bool) *tsnet.Server {
		cfg := tsnet.Config{
			Name: name, Port: testPort, Mcast: testMultiCastAddr, Target: tsnet.DefaultTarget,
			BaseBroadcastInterval: 100 * time.Millisecond, Identity: id, Storage: st,
			SyncRescan: 100 * time.Millisecond, Headless: headless,
			Sync: []tsnet.SyncFolder{{Name: "docs", Path: dir}},
		}
		srv := cfg.NewServer()
		if err := srv.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Stop)
		return srv
	}
	return mk("HostA", idA, storeA, dirA, false), mk("HostB", idB, storeB, dirB, true)
}

// syncBuffer is a log output safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.String()
}

// captureLogs collects the logs (also still sent to stderr) as plain text, until the end of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	sb := &syncBuffer{}
	saved := log.Config
	log.Config.JSON, log.Config.NoTimestamp, log.Config.ConsoleColor = false, true, false
	log.SetOutput(io.MultiWriter(os.Stderr, sb))
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.Config = saved
	})
	return sb
}

func TestHeadlessTrusted(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logs := captureLogs(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	writeTree(t, dirA, map[string]string{"a.txt": "from A"})
	writeTree(t, dirB, map[string]string{"b.txt": "from B"})
	srvA, srvB := headlessPair(ctx, t, true, dirA, dirB)
	// Trusted peers link by themselves and sync.
	syncStatus(ctx, t, srvB, "docs", "in sync", func(st tsnet.SyncStatus) bool {
		return inSync(st) && content(t, filepath.Join(dirB, "a.txt")) == "from A"
	})
	syncStatus(ctx, t, srvA, "docs", "in sync", func(st tsnet.SyncStatus) bool {
		return inSync(st) && content(t, filepath.Join(dirA, "b.txt")) == "from B"
	})
	// Clipboard and offers are ignored by the headless side.
	if _, err := srvA.SendClip("secret"); err != nil {
		t.Fatal(err)
	}
	offerPath := filepath.Join(t.TempDir(), "offer.txt")
	writeTree(t, filepath.Dir(offerPath), map[string]string{"offer.txt": "offered"})
	if _, err := srvA.OfferFile(offerPath); err != nil {
		t.Fatal(err)
	}
	// A change after those, once synced, proves they were handled (frames are in order).
	writeTree(t, dirA, map[string]string{"later.txt": "later"})
	syncStatus(ctx, t, srvB, "docs", "later synced", func(_ tsnet.SyncStatus) bool {
		return content(t, filepath.Join(dirB, "later.txt")) == "later"
	})
	for peer := range srvB.Peers.All() {
		if data, _ := srvB.Peers.Get(peer); data.Clip != "" {
			t.Errorf("headless kept a clipboard: %q", data.Clip)
		}
	}
	// What A pulled from headless B completes: A's saved confirmation is handled (that's what logs "sent").
	waitFor(ctx, t, "B logged sending b.txt", func() bool { return strings.Contains(logs.String(), `sent "b.txt"`) })
	for _, tr := range srvB.Transfers() {
		if tr.State == tsnet.TransferOffered {
			t.Errorf("headless recorded an offer: %+v", tr)
		}
	}
}

func TestHeadlessUntrusted(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srvA, srvB := headlessPair(ctx, t, false, t.TempDir(), t.TempDir())
	peerB := waitForPeer(ctx, t, srvA, "HostB")
	peerA := waitForPeer(ctx, t, srvB, "HostA")
	if err := srvA.ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // several handshake retries
	if data, _ := srvB.Peers.Get(peerA); data.Status != tsnet.NotLinked {
		t.Errorf("headless shouldn't have a pending request, status %v", data.Status)
	}
	if srvA.Linked(peerB) {
		t.Error("untrusted request linked")
	}
}
