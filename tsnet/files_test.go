package tsnet_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

// linkedPair starts 2 servers with the given configs, connects/accepts and waits for the link.
// Returns the servers and how B sees A (to accept A's offers).
func linkedPair(ctx context.Context, t *testing.T, cfgA, cfgB tsnet.Config) (*tsnet.Server, *tsnet.Server, tsnet.Peer) {
	t.Helper()
	for _, c := range []*tsnet.Config{&cfgA, &cfgB} {
		c.Port, c.Mcast, c.Target = testPort, testMultiCastAddr, tsnet.DefaultTarget
		c.BaseBroadcastInterval = 100 * time.Millisecond
		c.Identity, _ = tcrypto.NewIdentity()
	}
	serverA, serverB := cfgA.NewServer(), cfgB.NewServer()
	for _, srv := range []*tsnet.Server{serverA, serverB} {
		if err := srv.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Stop)
	}
	peerB := waitForPeer(ctx, t, serverA, cfgB.Name)
	peerA := waitForPeer(ctx, t, serverB, cfgA.Name)
	if err := serverA.ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	waitForStatus(ctx, t, serverB, peerA, tsnet.ReceivedConn)
	if err := serverB.ConnectToPeer(peerA); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, "A linked", func() bool { return serverA.Linked(peerB) })
	waitFor(ctx, t, "B linked", func() bool { return serverB.Linked(peerA) })
	return serverA, serverB, peerA
}

// waitTransfer waits for a transfer matching (incoming, name, state) on srv.
func waitTransfer(ctx context.Context, t *testing.T, srv *tsnet.Server, incoming bool,
	name string, state tsnet.TransferState,
) tsnet.Transfer {
	t.Helper()
	var res tsnet.Transfer
	waitFor(ctx, t, srv.Name+" transfer "+name+" "+state.String(), func() bool {
		for _, tr := range srv.Transfers() {
			if tr.Incoming == incoming && tr.Name == name && tr.State == state {
				res = tr
				return true
			}
		}
		return false
	})
	return res
}

func offerAndDownload(ctx context.Context, t *testing.T, serverA, serverB *tsnet.Server, peerA tsnet.Peer,
	path, savedAs string,
) tsnet.Transfer {
	t.Helper()
	before := len(serverB.Transfers())
	if n, err := serverA.OfferFile(path); err != nil || n != 1 {
		t.Fatalf("OfferFile(%s): %d %v", path, n, err)
	}
	var offered tsnet.Transfer
	waitFor(ctx, t, "B got the offer", func() bool {
		trs := serverB.Transfers()
		if len(trs) <= before {
			return false
		}
		offered = trs[len(trs)-1]
		return true
	})
	if offered.State != tsnet.TransferOffered || offered.Name != filepath.Base(path) {
		t.Fatalf("unexpected offer %+v", offered)
	}
	if err := serverB.AcceptFile(peerA, offered.ID); err != nil {
		t.Fatal(err)
	}
	if err := serverB.AcceptFile(peerA, offered.ID); err == nil {
		t.Errorf("accepting twice should fail")
	}
	waitTransfer(ctx, t, serverA, false, offered.Name, tsnet.TransferDone)
	return waitTransfer(ctx, t, serverB, true, savedAs, tsnet.TransferDone)
}

func TestFileTransfer(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirB := t.TempDir()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: dirB})
	src := t.TempDir()
	// More than a few chunks, not a multiple of the chunk size.
	content := make([]byte, 3*tsnet.FileChunkSize+12345)
	_, _ = rand.Read(content)
	path := filepath.Join(src, "data.bin")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	_ = os.Chtimes(path, mtime, mtime)
	empty := filepath.Join(src, "empty.txt")
	_ = os.WriteFile(empty, nil, 0o644)
	// Offered but never accepted: nothing written.
	if n, err := serverA.OfferFile(empty); err != nil || n != 1 {
		t.Fatalf("OfferFile: %d %v", n, err)
	}
	waitTransfer(ctx, t, serverB, true, "empty.txt", tsnet.TransferOffered)
	if entries, _ := os.ReadDir(dirB); len(entries) != 0 {
		t.Errorf("nothing should be written before accepting, got %v", entries)
	}
	offerAndDownload(ctx, t, serverA, serverB, peerA, path, "data.bin")
	offerAndDownload(ctx, t, serverA, serverB, peerA, empty, "empty.txt")
	tr := offerAndDownload(ctx, t, serverA, serverB, peerA, path, "data-1.bin") // no overwrite.
	if tr.Path != filepath.Join(dirB, "data-1.bin") || tr.Size != int64(len(content)) || tr.Done != tr.Size {
		t.Errorf("unexpected transfer %+v", tr)
	}
	for _, tc := range []struct {
		name    string
		content []byte
	}{{"data.bin", content}, {"empty.txt", nil}, {"data-1.bin", content}} {
		p := filepath.Join(dirB, tc.name)
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, tc.content) {
			t.Errorf("%s: content mismatch (%d vs %d bytes) %v", tc.name, len(got), len(tc.content), err)
			continue
		}
		fi, _ := os.Stat(p)
		if fi.Mode().Perm()&0o111 != 0 {
			t.Errorf("%s: should not be executable: %v", tc.name, fi.Mode())
		}
		if tc.name != "empty.txt" && !fi.ModTime().Equal(mtime) {
			t.Errorf("%s: mtime %v, expected %v", tc.name, fi.ModTime(), mtime)
		}
	}
	checkNoPartial(t, dirB)
	if _, err := serverA.OfferFile(src); err == nil {
		t.Errorf("expected error offering a directory")
	}
}

func checkNoPartial(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tsync-partial") {
			t.Errorf("leftover partial file %s", e.Name())
		}
	}
}

func TestFileRefusedOrChanged(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirB := t.TempDir()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: dirB, MaxFileSize: 1000})
	// Too large for B: refused locally on accept, never requested.
	big := filepath.Join(t.TempDir(), "big.bin")
	_ = os.WriteFile(big, make([]byte, 5000), 0o644)
	if _, err := serverA.OfferFile(big); err != nil {
		t.Fatal(err)
	}
	offered := waitTransfer(ctx, t, serverB, true, "big.bin", tsnet.TransferOffered)
	if err := serverB.AcceptFile(peerA, offered.ID); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected too large error, got %v", err)
	}
	waitTransfer(ctx, t, serverB, true, "big.bin", tsnet.TransferFailed)
	// Changed after being offered: sender aborts.
	small := filepath.Join(t.TempDir(), "small.txt")
	_ = os.WriteFile(small, []byte("v1"), 0o644)
	if _, err := serverA.OfferFile(small); err != nil {
		t.Fatal(err)
	}
	offered = waitTransfer(ctx, t, serverB, true, "small.txt", tsnet.TransferOffered)
	_ = os.WriteFile(small, []byte("v2 longer"), 0o644)
	if err := serverB.AcceptFile(peerA, offered.ID); err != nil {
		t.Fatal(err)
	}
	failed := waitTransfer(ctx, t, serverB, true, "small.txt", tsnet.TransferFailed)
	if !strings.Contains(failed.Err, "changed since it was offered") {
		t.Errorf("unexpected error %q", failed.Err)
	}
	waitTransfer(ctx, t, serverA, false, "small.txt", tsnet.TransferFailed)
	if entries, _ := os.ReadDir(dirB); len(entries) != 0 {
		t.Errorf("nothing should be written, got %v", entries)
	}
}
