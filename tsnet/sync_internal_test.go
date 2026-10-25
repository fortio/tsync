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
	chunks := indexChunks("photos", entries, ig, 300)
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
		if i == 0 { // our ignore rules first.
			if b[0] != 1 || int(b[1])<<8|int(b[2]) != 4 || string(b[3:7]) != "*.o\n" {
				t.Fatalf("unexpected rules header %q", b[:7])
			}
			b = b[7:]
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
	empty := indexChunks("x", nil, nil, maxSyncChunk) // default rules: not custom, empty.
	if len(empty) != 1 || empty[0][2] != syncIndexFirst|syncIndexLast || len(empty[0]) != 6 || empty[0][3] != 0 {
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
