package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fortio.org/progressbar"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

func TestShortPath(t *testing.T) {
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
	if len(lines) != 2 || !strings.Contains(lines[0].text, "offered "+f+" (2 Kb), will be sent to peers when they connect") ||
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
			`leno16 offers C:\x\Config.wtf (10 b): click or press d to download`,
		},
		{
			tsnet.Transfer{
				Peer: peer, Incoming: true, Name: "a.bin", Size: 2048, Done: 1024, State: tsnet.TransferActive,
				Start: time.Now().Add(-time.Second),
			},
			"1 Kb out of 2 Kb, 1s elapsed",
		},
		{
			tsnet.Transfer{
				Peer: peer, Incoming: true, Name: "a.bin", Size: 2048, Path: "/tmp/a.bin", State: tsnet.TransferDone,
				Start: time.Unix(10, 0), End: time.Unix(12, 0),
			},
			"a.bin from leno16 saved as /tmp/a.bin (2 Kb in 2s, 1 Kb/s)",
		},
		{tsnet.Transfer{Peer: peer, Name: "b", Size: 4, Done: 4, State: tsnet.TransferActive}, "· b to leno16"},
		{tsnet.Transfer{Peer: peer, Name: "b", Size: 4, State: tsnet.TransferDone}, "b sent to leno16 (4 b)"},
		{tsnet.Transfer{Peer: peer, Name: "b", State: tsnet.TransferFailed, Err: "boom"}, "b to leno16: boom"},
		{tsnet.Transfer{Peer: peer, Incoming: true, Name: "c", State: tsnet.TransferFailed, Err: "bad"}, "c from leno16: bad"},
	} {
		l := fp.transferLine(tc.tr)
		if !strings.Contains(l.text, tc.expect) {
			t.Errorf("transferLine(%v) = %q, expected %q", tc.tr.State, l.text, tc.expect)
		}
		if tc.tr.State == tsnet.TransferActive && (!strings.Contains(l.lead, "%") || l.leadW < barWidth) {
			t.Errorf("active line should have a progress bar lead: %q (%d)", l.lead, l.leadW)
		}
		if (l.offer != nil) != (tc.tr.State == tsnet.TransferOffered) {
			t.Errorf("offer set wrong for %v", tc.tr.State)
		}
	}
	if len(fp.bars) != 2 { // the 2 active transfers above.
		t.Errorf("expected 2 progress bars, got %d", len(fp.bars))
	}
	fp.Lines() // no active transfers on the server: bars are forgotten.
	if len(fp.bars) != 0 {
		t.Errorf("expected bars of finished transfers to be removed, got %d", len(fp.bars))
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

func TestFilePanelOfferMaxRate(t *testing.T) {
	ap, _, _ := newTestAP(100, 30)
	id, _ := tcrypto.NewIdentity()
	srv := (&tsnet.Config{Name: "test", Identity: id, MaxRate: 50 * 1024}).NewServer()
	fp := NewFilePanel(ap, srv, t.TempDir(), 5)
	f := filepath.Join(t.TempDir(), "doc.pdf")
	_ = os.WriteFile(f, make([]byte, 2048), 0o644)
	fp.Offer([]string{f})
	lines := fp.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0].text, "offered "+f+" (2 Kb), will be sent (at max 50 Kb/s) to peers") {
		t.Errorf("unexpected local lines %+v", lines)
	}
}

func TestFilePanelOfferDir(t *testing.T) {
	ap, _, _ := newTestAP(100, 30)
	id, _ := tcrypto.NewIdentity()
	srv := (&tsnet.Config{Name: "test", Identity: id}).NewServer()
	fp := NewFilePanel(ap, srv, t.TempDir(), 5)
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "a"), make([]byte, 2048), 0o644)
	_ = os.WriteFile(filepath.Join(d, "b"), []byte("x"), 0o644)
	fp.Offer([]string{d})
	lines := fp.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0].text, "offered "+d+" (2 files, ") {
		t.Errorf("unexpected local lines %+v", lines)
	}
}

func TestFilePanelCurFileLine(t *testing.T) {
	ap, _, _ := newTestAP(100, 30)
	id, _ := tcrypto.NewIdentity()
	srv := (&tsnet.Config{Name: "test", Identity: id}).NewServer()
	fp := NewFilePanel(ap, srv, t.TempDir(), 5)
	tr := tsnet.Transfer{
		Peer: tsnet.Peer{Name: "p"}, Incoming: true, Dir: true, Files: 3, Name: "proj", Size: 4096, Done: 1024,
		State: tsnet.TransferActive, CurFile: "sub/deep/rand.bin", CurDone: 512, CurSize: 2048,
	}
	l := fp.curFileLine(tr, progressbar.GreenBar)
	if !strings.Contains(l.text, "2 Kb of sub/deep/rand.bin") || !strings.Contains(l.lead, "25.0%") {
		t.Errorf("unexpected file line %+v", l)
	}
}
