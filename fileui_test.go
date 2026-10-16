package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

func TestHumanSizeAndBar(t *testing.T) {
	for _, tc := range []struct {
		n   int64
		out string
	}{{0, "0 bytes"}, {1, "1 byte"}, {1023, "1023 bytes"}, {1536, "1.5 KiB"}, {5 << 20, "5.0 MiB"}, {3 << 30, "3.0 GiB"}} {
		if got := HumanSize(tc.n); got != tc.out {
			t.Errorf("HumanSize(%d) = %q, expected %q", tc.n, got, tc.out)
		}
	}
	if got := progressBar(1, 4, 8); got != "██░░░░░░  25%" {
		t.Errorf("unexpected bar %q", got)
	}
	if got := progressBar(0, 0, 4); got != "████ 100%" {
		t.Errorf("unexpected bar for empty file %q", got)
	}
	home, _ := os.UserHomeDir()
	if got := ShortPath(filepath.Join(home, "Downloads", "x")); got != "~"+string(filepath.Separator)+filepath.Join("Downloads", "x") {
		t.Errorf("unexpected short path %q", got)
	}
}

func TestFilePanel(t *testing.T) {
	ap, buf, bw := newTestAP(100, 30)
	id, _ := tcrypto.NewIdentity()
	srv := (&tsnet.Config{Name: "test", Identity: id}).NewServer()
	fp := NewFilePanel(ap, srv, t.TempDir(), 5)
	if lines := fp.Lines(); len(lines) != 0 || fp.Draw(0, lines) != 0 {
		t.Errorf("expected no lines/box initially")
	}
	f := filepath.Join(t.TempDir(), "doc.pdf")
	_ = os.WriteFile(f, make([]byte, 2048), 0o644)
	fp.Offer([]string{f, filepath.Join(t.TempDir(), "missing")})
	lines := fp.Lines()
	if len(lines) != 2 || !strings.Contains(lines[0].text, "offered "+f+" (2.0 KiB) to 0 peers") ||
		!strings.Contains(lines[1].text, "can't offer") {
		t.Errorf("unexpected local lines %+v", lines)
	}
	if h := fp.Draw(10, lines); h != 4 {
		t.Errorf("unexpected box height %d", h)
	}
	_ = bw.Flush()
	if !strings.Contains(buf.String(), "files · saved in") {
		t.Errorf("missing box title in %q", buf.String())
	}
	if fp.Clicked(12) {
		t.Errorf("clicking a non offer line should do nothing")
	}
	peer := tsnet.Peer{Name: "leno16"}
	for _, tc := range []struct {
		tr     tsnet.Transfer
		expect string
	}{
		{
			tsnet.Transfer{Peer: peer, Incoming: true, Source: `C:\x\Config.wtf`, Size: 10, State: tsnet.TransferOffered},
			`leno16 offers C:\x\Config.wtf (10 bytes): click or press d to download`,
		},
		{
			tsnet.Transfer{Peer: peer, Incoming: true, Name: "a.bin", Size: 2048, Done: 1024, State: tsnet.TransferActive},
			"a.bin ██████████░░░░░░░░░░  50% 1.0 KiB/2.0 KiB from leno16",
		},
		{
			tsnet.Transfer{Peer: peer, Incoming: true, Name: "a.bin", Size: 1, Path: "/tmp/a.bin", State: tsnet.TransferDone},
			"a.bin (1 byte) from leno16 saved as /tmp/a.bin",
		},
		{tsnet.Transfer{Peer: peer, Name: "b", Size: 4, Done: 4, State: tsnet.TransferActive}, "b ████████████████████ 100% to leno16"},
		{tsnet.Transfer{Peer: peer, Name: "b", Size: 4, State: tsnet.TransferDone}, "b (4 bytes) sent to leno16"},
		{tsnet.Transfer{Peer: peer, Name: "b", State: tsnet.TransferFailed, Err: "boom"}, "b to leno16: boom"},
		{tsnet.Transfer{Peer: peer, Incoming: true, Name: "c", State: tsnet.TransferFailed, Err: "bad"}, "c from leno16: bad"},
	} {
		l := transferLine(tc.tr)
		if !strings.Contains(l.text, tc.expect) {
			t.Errorf("transferLine(%v) = %q, expected %q", tc.tr.State, l.text, tc.expect)
		}
		if (l.offer != nil) != (tc.tr.State == tsnet.TransferOffered) {
			t.Errorf("offer set wrong for %v", tc.tr.State)
		}
	}
	// Max lines kept.
	for range 10 {
		fp.Offer([]string{f})
	}
	if lines = fp.Lines(); len(lines) != 5 {
		t.Errorf("expected max 5 lines, got %d", len(lines))
	}
	fp.DownloadLatest() // no pending offer: just a warning.
}
