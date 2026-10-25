package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

func TestParseSyncFolder(t *testing.T) {
	dir := t.TempDir()
	f, err := ParseSyncFolder("docs=" + dir)
	if err != nil || f.Name != "docs" || f.Path != dir {
		t.Errorf("unexpected %+v %v", f, err)
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if f, err = ParseSyncFolder("home=~/"); err != nil || f.Path != filepath.Clean(home) {
			t.Errorf("~/ should be the home directory: %+v %v", f, err)
		}
	}
	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, nil, 0o644)
	for _, bad := range []string{"docs", "docs=", "bad name=" + dir, "x=" + file, "x=" + filepath.Join(dir, "missing")} {
		if _, err = ParseSyncFolder(bad); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestSyncLine(t *testing.T) {
	peer := tsnet.Peer{Name: "bob\x1b[31m"}
	var conflict syncdir.Summary
	conflict.Counts[syncdir.Conflict] = 1
	conflict.Conflicts = []string{"x"}
	var inSync syncdir.Summary
	inSync.Counts[syncdir.InSync] = 3
	var pull syncdir.Summary
	pull.Counts[syncdir.Pull] = 2
	pull.PullBytes = 2048
	ready := func(sum syncdir.Summary) tsnet.SyncStatus {
		return tsnet.SyncStatus{Folder: "d", Scanned: true, Peer: peer, Ready: true, Summary: sum}
	}
	getting := func(sum syncdir.Summary) tsnet.SyncStatus {
		st := ready(sum)
		st.Getting, st.CurFile, st.CurDone, st.CurSize = 1, "sub/f", 256, 1024
		return st
	}
	listed := func(sum syncdir.Summary) tsnet.SyncStatus {
		st := ready(sum)
		st.ConflictsFile = "/x/conflicts.txt"
		return st
	}
	const listedX = " (list in /x/conflicts.txt)"
	copies := func(sum syncdir.Summary) tsnet.SyncStatus {
		st := listed(sum)
		st.ConflictCopies = 2
		return st
	}
	cantStore := func(sum syncdir.Summary) tsnet.SyncStatus {
		st := listed(sum)
		st.CantStoreHere = 2
		return st
	}
	skewed := func(sum syncdir.Summary) tsnet.SyncStatus {
		st := ready(sum)
		st.ClockSkew = -2500 * time.Millisecond
		return st
	}
	for _, tc := range []struct {
		st    tsnet.SyncStatus
		text  string
		color tcolor.BasicColor
	}{
		{tsnet.SyncStatus{Folder: "d", Err: "boom"}, "📁 d: ❌ boom", tcolor.BrightRed},
		{tsnet.SyncStatus{Folder: "d", Path: "/x"}, "📁 d: scanning /x…", tcolor.Cyan},
		{tsnet.SyncStatus{Folder: "d", Scanned: true, Files: 4}, "📁 d (4 items): no linked peer has it", tcolor.Cyan},
		{tsnet.SyncStatus{Folder: "d", Scanned: true, Peer: peer}, "📁 d ↔ bob: waiting for its list", tcolor.Cyan},
		{ready(inSync), "📁 d ↔ bob · in sync (3 items)", tcolor.BrightGreen},
		{ready(conflict), "📁 d ↔ bob · 1 conflict", tcolor.BrightYellow},
		{ready(pull), "📁 d ↔ bob · 2 to get (2 Kb)", tcolor.BrightCyan},
		{getting(pull), "📁 d ↔ bob · ⬇ sub/f 25% · 2 to get (2 Kb)", tcolor.BrightCyan},
		{listed(conflict), "📁 d ↔ bob · 1 conflict (list in /x/conflicts.txt)", tcolor.BrightYellow},
		{copies(inSync), "📁 d ↔ bob · in sync (3 items) · 2 conflict copies to review" + listedX, tcolor.BrightYellow},
		{skewed(inSync), "📁 d ↔ bob · in sync (3 items) · ⚠ clocks differ by 2.5s", tcolor.BrightYellow},
		{cantStore(inSync), "📁 d ↔ bob · in sync (3 items) · 2 from bob can't be stored here" + listedX, tcolor.BrightYellow},
	} {
		l := SyncLine(tc.st)
		if l.text != tc.text || l.color != tc.color {
			t.Errorf("got %q (%v), want %q (%v)", l.text, l.color, tc.text, tc.color)
		}
	}
}

func TestSyncPanel(t *testing.T) {
	ap, buf, bw := newTestAP(100, 30)
	id, _ := tcrypto.NewIdentity()
	srv := (&tsnet.Config{Name: "test", Identity: id}).NewServer()
	sp := NewSyncPanel(ap, srv)
	if lines := sp.Lines(); len(lines) != 0 || sp.Draw(0, lines) != 0 {
		t.Errorf("expected no box without sync folders")
	}
	if h := sp.Draw(5, []syncLine{{text: "📁 a", color: tcolor.Cyan}, {text: "📁 b\x1b]52;c;eA==\a", color: tcolor.Cyan}}); h != 4 {
		t.Errorf("unexpected height %d", h)
	}
	_ = bw.Flush()
	if out := buf.String(); !strings.Contains(out, "🔄 sync") || strings.Contains(out, "]52;") {
		t.Errorf("unexpected output %q", out)
	}
}
