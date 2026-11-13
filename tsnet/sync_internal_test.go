package tsnet

import (
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
)

func TestIndexChunks(t *testing.T) {
	entries := make(map[string]syncdir.Entry)
	for i := range 50 {
		p := "dir/file" + strconv.Itoa(i)
		entries[p] = syncdir.Entry{
			Path: p, Mode: 0o644, Size: int64(i), MTime: time.Unix(1700000000, int64(i)), Hash: syncdir.Hash{byte(i)},
		}
	}
	entries["dir"] = syncdir.Entry{Path: "dir", Dir: true, Mode: 0o755, MTime: time.Unix(1600000000, 0)}
	ig, err := syncdir.ParseIgnore("*.o\n")
	if err != nil {
		t.Fatal(err)
	}
	chunks := indexChunks("photos", entries, ig, []string{"a:b", "c?"}, 300)
	if len(chunks) < 5 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	got := make(map[string]syncdir.Entry)
	for i, c := range chunks {
		if c[0] != 6 || string(c[1:7]) != "photos" {
			t.Fatalf("chunk %d: bad header %q", i, c[:8])
		}
		flags := c[7]
		if (flags&syncIndexFirst != 0) != (i == 0) || (flags&syncIndexLast != 0) != (i == len(chunks)-1) {
			t.Errorf("chunk %d/%d: unexpected flags %d", i, len(chunks), flags)
		}
		b := c[8:]
		if i == 0 { // our ignore rules and the paths we can't store first.
			rules, cantStore, rest, herr := decodeHeader(b)
			if herr != nil {
				t.Fatal(herr)
			}
			if text, custom := rules.Text(); text != "*.o\n" || !custom || len(cantStore) != 2 || !cantStore["a:b"] || !cantStore["c?"] {
				t.Fatalf("unexpected header %q %v %v", text, custom, cantStore)
			}
			b = rest
		}
		es, err := decodeEntries(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			got[e.Path] = e
		}
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for p, e := range entries {
		if !sameEntry(got[p], e) {
			t.Errorf("%s: got %+v, want %+v", p, got[p], e)
		}
	}
	empty := indexChunks("x", nil, nil, nil, maxSyncChunk) // default rules: not custom, empty; nothing we can't store.
	if len(empty) != 1 || empty[0][2] != syncIndexFirst|syncIndexLast || len(empty[0]) != 8 || empty[0][3] != 0 {
		t.Errorf("unexpected empty index chunks %q", empty)
	}
}

func TestDecodeEntriesErrors(t *testing.T) {
	good := encodeEntry(nil, syncdir.Entry{Path: "ok", Mode: 0o644})
	if _, err := decodeEntries(good); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"bad kind":  append([]byte{'x'}, good[1:]...),
		"truncated": good[:len(good)-1],
		"bad path":  encodeEntry(nil, syncdir.Entry{Path: "../up", Mode: 0o644}),
		"nfd path":  encodeEntry(nil, syncdir.Entry{Path: "é", Mode: 0o644}),
		"no hash":   good[:25],
	} {
		if _, err := decodeEntries(b); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateSyncName(t *testing.T) {
	for _, ok := range []string{"photos", "my-docs_2.0"} {
		if err := ValidateSyncName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".hidden", "a b", "a/b", "é", "x\ny", strings.Repeat("a", MaxSyncNameLen+1)} {
		if ValidateSyncName(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestSyncFoldersClock(t *testing.T) {
	s := (&Config{Name: "test"}).NewServer()
	s.syncFolders["docs"] = &syncFolder{SyncFolder: SyncFolder{Name: "docs"}}
	l := &link{}
	payload := binary.BigEndian.AppendUint64([]byte{FrameSyncFolders}, uint64(time.Now().Add(-3*time.Second).UnixNano()))
	if !s.handleSyncFrame(Peer{Name: "peer"}, l, nil, append(payload, "docs\nother\nbad name"...)) {
		t.Fatal("not handled")
	}
	if l.clockSkew < 3*time.Second || l.clockSkew > 4*time.Second {
		t.Errorf("unexpected skew %v", l.clockSkew)
	}
	if !l.syncFolders["docs"] || !l.syncFolders["other"] || len(l.syncFolders) != 2 {
		t.Errorf("unexpected folders %v", l.syncFolders)
	}
}

func TestFilterWindowsNames(t *testing.T) {
	entries := []syncdir.Entry{{Path: "ok.txt"}, {Path: "a:b"}, {Path: "dir/con.txt"}, {Path: "dir/fine"}}
	kept, skipped := filterWindowsNames(entries)
	if len(kept) != 2 || kept[0].Path != "ok.txt" || kept[1].Path != "dir/fine" || len(skipped) != 2 {
		t.Errorf("unexpected %+v %q", kept, skipped)
	}
}

func TestNewWindowsSkips(t *testing.T) {
	f := &syncFolder{}
	if added := f.newWindowsSkips([]string{"a:b", "c?"}); len(added) != 2 {
		t.Errorf("expected 2 new, got %q", added)
	}
	if added := f.newWindowsSkips([]string{"c?", "a:b"}); len(added) != 0 { // eg the same from another peer.
		t.Errorf("expected none new, got %q", added)
	}
	if added := f.newWindowsSkips([]string{"a:b", "d|e"}); len(added) != 1 || added[0] != "d|e" {
		t.Errorf("expected only d|e, got %q", added)
	}
}

func TestSkewTooBig(t *testing.T) {
	ms := time.Millisecond
	for _, tc := range []struct {
		skew, maxSkew time.Duration
		want          bool
	}{
		{0, 0, false},
		{1000 * ms, 0, false},
		{1001 * ms, 0, true},
		{-1500 * ms, 0, true},
		{700 * ms, 500 * ms, true},
		{-700 * ms, 500 * ms, true},
		{400 * ms, 500 * ms, false},
		{2 * time.Second, 5 * time.Second, false},
	} {
		if got := SkewTooBig(tc.skew, tc.maxSkew); got != tc.want {
			t.Errorf("SkewTooBig(%v, %v) = %v, want %v", tc.skew, tc.maxSkew, got, tc.want)
		}
	}
}

func TestSkewLimit(t *testing.T) {
	if got := skewLimit(0); got != DefaultMaxClockSkew {
		t.Errorf("skewLimit(0) = %v, want %v", got, DefaultMaxClockSkew)
	}
	if got := skewLimit(-time.Second); got != DefaultMaxClockSkew {
		t.Errorf("skewLimit(-1s) = %v, want %v", got, DefaultMaxClockSkew)
	}
	if got := skewLimit(300 * time.Millisecond); got != 300*time.Millisecond {
		t.Errorf("skewLimit(300ms) = %v, want 300ms", got)
	}
}

func TestNoteSkew(t *testing.T) {
	ms := time.Millisecond
	s := &Server{Config: Config{SyncMaxClockSkew: 500 * ms}}
	l := &link{}
	peer := Peer{Name: "p"}
	note := func(sample time.Duration) {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		s.noteSkewLocked(peer, l, sample)
	}
	note(100 * ms)
	if l.clockSkew != 100*ms || l.skewWarned {
		t.Errorf("first sample: skew %v warned %v", l.clockSkew, l.skewWarned)
	}
	// a delayed sample doesn't count: the smallest of the recent ones is the estimate.
	note(900 * ms)
	if l.clockSkew != 100*ms || l.skewWarned {
		t.Errorf("delayed sample: skew %v warned %v", l.clockSkew, l.skewWarned)
	}
	// the clock really moves: after skewWindow samples the old ones are gone.
	for range skewWindow {
		note(700 * ms)
	}
	if l.clockSkew != 700*ms || !l.skewWarned {
		t.Errorf("moved clock: skew %v warned %v", l.clockSkew, l.skewWarned)
	}
	// back below the limit but not by the margin: still warned (no flapping).
	for range skewWindow {
		note(450 * ms)
	}
	if !l.skewWarned {
		t.Errorf("450ms with a 500ms limit should not clear the warning yet")
	}
	for range skewWindow {
		note(300 * ms)
	}
	if l.skewWarned || l.clockSkew != 300*ms {
		t.Errorf("back to normal: skew %v warned %v", l.clockSkew, l.skewWarned)
	}
	// the peer's clock ahead of ours (negative): the smallest value is the real one too.
	l = &link{}
	note(-1000 * ms)
	note(-950 * ms)
	if l.clockSkew != -1000*ms || !l.skewWarned {
		t.Errorf("negative: skew %v warned %v", l.clockSkew, l.skewWarned)
	}
}

func TestSkewWords(t *testing.T) {
	for _, tc := range []struct {
		skew time.Duration
		want string
	}{
		{15 * time.Millisecond, "15ms behind ours"},
		{-15 * time.Millisecond, "15ms ahead of ours"},
		{-1151 * time.Millisecond, "1.151s ahead of ours"},
		{0, "0s behind ours"},
	} {
		if got := SkewWords(tc.skew); got != tc.want {
			t.Errorf("SkewWords(%v) = %q, want %q", tc.skew, got, tc.want)
		}
	}
}
