package tsnet

import (
	"os"
	"path/filepath"
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

func TestTrash(t *testing.T) {
	dir := t.TempDir()
	r := openRoot(t, dir)
	target := filepath.Join("sub", "f.txt")
	for _, c := range []string{"v1", "v2", "v3"} {
		writeAt(t, filepath.Join(dir, target), c, time.Time{})
		if err := toTrash(r, target, "sub/f.txt", 2); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct version names.
	}
	trashDir := filepath.Join(syncdir.TrashDir, "sub")
	versions := trashVersions(r, filepath.ToSlash(trashDir), "f.txt")
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %v", versions)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, trashDir, versions[1])); string(b) != "v3" {
		t.Errorf("latest version should be v3, got %q", b)
	}
	// Over the size limit: the oldest versions go, but not the latest of each file.
	writeAt(t, filepath.Join(dir, "g"), strings.Repeat("g", 100), time.Time{})
	if err := toTrash(r, "g", "g", 5); err != nil {
		t.Fatal(err)
	}
	pruneTrash(dir, 10)
	if v := trashVersions(r, filepath.ToSlash(trashDir), "f.txt"); len(v) != 1 || v[0] != versions[1] {
		t.Errorf("expected only the latest f.txt version, got %v", v)
	}
	if v := trashVersions(r, syncdir.TrashDir, "g"); len(v) != 1 {
		t.Errorf("the only version of g should stay, got %v", v)
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
