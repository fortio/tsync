package tsnet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
)

func writeAt(t *testing.T, p, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestCheckUnchanged(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	writeAt(t, filepath.Join(dir, "f"), "aaaa", old)
	idx, err := syncdir.Scan(dir, syncdir.Index{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := idx.Entries["f"]
	r := openRoot(t, dir)
	if err = checkUnchanged(r, "f", &e); err != nil {
		t.Errorf("unchanged file: %v", err)
	}
	if err = checkUnchanged(r, "f", nil); err == nil || !strings.Contains(err.Error(), "created") {
		t.Errorf("expected absent: %v", err)
	}
	if err = checkUnchanged(r, "missing", nil); err != nil {
		t.Errorf("absent as expected: %v", err)
	}
	writeAt(t, filepath.Join(dir, "f"), "bbbb", time.Time{}) // same size, new time: hashed.
	if err = checkUnchanged(r, "f", &e); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Errorf("expected modified: %v", err)
	}
	writeAt(t, filepath.Join(dir, "f"), "aaaa", time.Time{}) // same content, new time: fine.
	if err = checkUnchanged(r, "f", &e); err != nil {
		t.Errorf("same content: %v", err)
	}
	if err = os.Remove(filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	if err = checkUnchanged(r, "f", &e); err == nil {
		t.Errorf("expected an error for a deleted file")
	}
}

func TestKeepOld(t *testing.T) {
	dir := t.TempDir()
	r := openRoot(t, dir)
	target := filepath.Join("sub", "f.txt")
	writeAt(t, filepath.Join(dir, target), "v1", time.Time{})
	// Replaced like sync does: the old version kept (hard link when possible), then the new one renamed over it.
	for _, c := range []string{"v2", "v3", "v4"} {
		if err := keepOld(r, target, "sub/f.txt", 2, false); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, "sub", ".new")
		writeAt(t, tmp, c, time.Time{})
		if err := os.Rename(tmp, filepath.Join(dir, target)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct version names.
	}
	if b, _ := os.ReadFile(filepath.Join(dir, target)); string(b) != "v4" {
		t.Errorf("current version should be v4, got %q", b)
	}
	oldDir := filepath.Join(syncdir.OldDir, "sub")
	versions := oldVersions(r, filepath.ToSlash(oldDir), "f.txt")
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %v", versions)
	}
	for i, want := range []string{"v2", "v3"} { // v1, the oldest, was removed.
		if b, _ := os.ReadFile(filepath.Join(dir, oldDir, versions[i])); string(b) != want {
			t.Errorf("version %d: got %q, want %q", i, b, want)
		}
	}
}

func TestSyncGetRefused(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	writeAt(t, filepath.Join(dir, "old"), "old", old)
	writeAt(t, filepath.Join(dir, "new"), "new", time.Time{})
	idx, err := syncdir.Scan(dir, syncdir.Index{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := (&Config{Name: "test"}).NewServer()
	s.syncFolders["docs"] = &syncFolder{SyncFolder: SyncFolder{Name: "docs", Path: dir}, local: idx}
	peer := Peer{Name: "peer"}
	l := &link{outgoing: map[uint32]*Transfer{1: {}}, syncFolders: map[string]bool{"docs": true}}
	notAnnounced := &link{outgoing: map[uint32]*Transfer{}, syncFolders: map[string]bool{}}
	for _, tc := range []struct {
		l          *link
		id         uint32
		name, path string
		hash       syncdir.Hash
		want       string
	}{
		{l, 2, "other", "old", idx.Entries["old"].Hash, "unknown sync folder"},
		{notAnnounced, 2, "docs", "old", idx.Entries["old"].Hash, "unknown sync folder"},
		{l, 2, "docs", "missing", syncdir.Hash{}, "changed since"},
		{l, 2, "docs", "old", syncdir.Hash{1}, "changed since"},
		{l, 2, "docs", "new", idx.Entries["new"].Hash, "still being written"},
		{l, 1, "docs", "old", idx.Entries["old"].Hash, reasonAlreadySending},
	} {
		if got := s.startSyncSendLocked(peer, tc.l, nil, tc.id, tc.name, tc.hash, tc.path); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

func TestSyncRetryBackoff(t *testing.T) {
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		n    int
		want time.Duration
	}{{1, syncRetry}, {2, 2 * syncRetry}, {3, 4 * syncRetry}, {7, 64 * syncRetry}, {50, 64 * syncRetry}} {
		if got := (syncFailure{at: at, n: tc.n}).retryAt().Sub(at); got != tc.want {
			t.Errorf("%d failures: retry after %v, want %v", tc.n, got, tc.want)
		}
	}
}

func TestIssueLines(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	file := &syncdir.Entry{Size: 2048, MTime: when}
	dir := &syncdir.Entry{Dir: true}
	f := &syncFolder{peers: map[Peer]*syncPeer{
		{Name: "zed"}: {issues: []syncdir.Item{{Path: "a", Action: syncdir.Conflict, Local: file, Remote: dir}}},
		{Name: "bob"}: {issues: []syncdir.Item{
			{Path: "b\tc", Action: syncdir.Conflict, Remote: file},
			{Path: "a", Action: syncdir.DeletionHeld, Local: file},
		}},
	}}
	got := issueLinesLocked(f)
	want := []string{
		"deletion held\ta\there: 2 Kb, 2026-01-02 03:04:05\tbob: absent",
		"conflict\ta\there: 2 Kb, 2026-01-02 03:04:05\tzed: directory",
		"conflict\t\"b\\tc\"\there: absent\tbob: 2 Kb, 2026-01-02 03:04:05",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}
