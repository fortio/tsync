package tsnet

import (
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
	chunks := indexChunks("photos", entries, 300)
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
		es, err := decodeEntries(c[8:])
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
	empty := indexChunks("x", nil, maxSyncChunk)
	if len(empty) != 1 || empty[0][2] != syncIndexFirst|syncIndexLast || len(empty[0]) != 3 {
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
