package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fortio.org/smap"
	"fortio.org/tsync/tsnet"
)

func TestSplitList(t *testing.T) {
	if got := SplitList(" a, b:12 ,,c "); strings.Join(got, "|") != "a|b:12|c" {
		t.Errorf("unexpected %q", got)
	}
	if got := SplitList(""); got != nil {
		t.Errorf("unexpected %q", got)
	}
}

func TestPendingBannerAndBell(t *testing.T) {
	peers := []smap.KV[tsnet.Peer, tsnet.PeerData]{
		{Key: tsnet.Peer{Name: "a"}, Value: tsnet.PeerData{Status: tsnet.Connected}},
		{Key: tsnet.Peer{Name: "b"}, Value: tsnet.PeerData{Status: tsnet.NotLinked}},
	}
	if _, ok := PendingBanner(peers); ok {
		t.Errorf("no banner expected without pending requests")
	}
	ap, buf, bw := newTestAP(100, 30)
	top := &TopArea{ap: ap}
	bells := func() int {
		_ = bw.Flush()
		n := strings.Count(buf.String(), "\a")
		buf.Reset()
		return n
	}
	top.bellOnNewPending(peers)
	if n := bells(); n != 0 {
		t.Errorf("unexpected bell without pending requests: %d", n)
	}
	peers[1].Value = tsnet.PeerData{Status: tsnet.ReceivedConn, HumanHash: "123-4567"}
	msg, ok := PendingBanner(peers)
	if !ok || msg != " ⚠ b (123-4567) wants to connect: check the hash, then click or press 2 to accept " {
		t.Errorf("unexpected banner %q", msg)
	}
	top.bellOnNewPending(peers)
	top.bellOnNewPending(peers) // repaint: still the same pending request, no new bell.
	if n := bells(); n != 1 {
		t.Errorf("expected 1 bell for 1 new pending request, got %d", n)
	}
	peers[0].Value.Status = tsnet.ReceivedConn
	msg, _ = PendingBanner(peers)
	if !strings.Contains(msg, " a (") || !strings.HasSuffix(msg, "(+1 more) ") {
		t.Errorf("unexpected banner for 2 pending requests %q", msg)
	}
	top.bellOnNewPending(peers)
	if n := bells(); n != 1 {
		t.Errorf("expected 1 bell for the 2nd new pending request, got %d", n)
	}
}

func TestPeerLine(t *testing.T) {
	peer := tsnet.Peer{Name: "host\x1b[31m", IP: "10.0.0.2"}
	line := PeerLine(1, peer, tsnet.PeerData{Port: 12, HumanHash: "123-4567"}, true)
	if !strings.Contains(line[1], "🔗 host (old version)") {
		t.Errorf("unexpected name cell %q", line[1])
	}
	line = PeerLine(2, peer, tsnet.PeerData{RunID: "r", Status: tsnet.ReceivedConn}, false)
	if strings.Contains(line[1], "old version") || !strings.Contains(line[0], "\x1b[5m") {
		t.Errorf("unexpected line %q", line)
	}
}

func TestCheckFilesAndDownloadDir(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "ok.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if err := CheckFiles([]string{f}); err != nil {
		t.Errorf("unexpected error %v", err)
	}
	for _, bad := range [][]string{{dir}, {f, filepath.Join(dir, "missing")}} {
		if err := CheckFiles(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	if DownloadDir(dir) != dir || DownloadDir("") == "" {
		t.Errorf("unexpected download dirs")
	}
}
