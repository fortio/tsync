package syncdir_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
)

func write(t *testing.T, root, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
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

func scan(t *testing.T, root string, prev syncdir.Index) syncdir.Index {
	t.Helper()
	idx, err := syncdir.Scan(root, prev, nil)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func hashOf(content string) syncdir.Hash {
	return sha256.Sum256([]byte(content))
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	write(t, root, "a.txt", "aaaa", old)
	write(t, root, "sub/deep/b.txt", "bee", old)
	write(t, root, ".git/config", "hidden", old)
	write(t, root, ".tsyncignore", "", old) // only it is not ignored by the default rules.
	write(t, root, "été.txt", "nfd", old)
	skipped := 0
	if runtime.GOOS != "windows" {
		if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "a.txt"), 0o775); err != nil {
			t.Fatal(err)
		}
		skipped = 1
	}
	idx := scan(t, root, syncdir.Index{})
	want := []string{".tsyncignore", "a.txt", "sub", "sub/deep", "sub/deep/b.txt", "été.txt"}
	if len(idx.Entries) != len(want) || idx.Skipped != skipped {
		t.Errorf("got %d entries (%d skipped), want %v (%d): %+v", len(idx.Entries), idx.Skipped, want, skipped, idx.Entries)
	}
	for _, k := range want {
		if e, ok := idx.Entries[k]; !ok || e.Path != k {
			t.Errorf("missing %q: %+v", k, e)
		}
	}
	a := idx.Entries["a.txt"]
	if a.Dir || a.Size != 4 || a.Hash != hashOf("aaaa") || !a.MTime.Equal(old) {
		t.Errorf("unexpected a.txt entry %+v", a)
	}
	if runtime.GOOS != "windows" && (a.Mode != 0o755 || idx.Entries["sub"].Mode != 0o755) {
		t.Errorf("unexpected modes %v %v (group/other write isn't kept)", a.Mode, idx.Entries["sub"].Mode)
	}
	if !idx.Entries["sub"].Dir || idx.Entries["été.txt"].Hash != hashOf("nfd") {
		t.Errorf("unexpected entries %+v", idx.Entries)
	}
}

func TestScanFastPath(t *testing.T) {
	root := t.TempDir()
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	write(t, root, "f", "aaaa", old)
	first := scan(t, root, syncdir.Index{})
	// Same size and time: not hashed again (we can tell, as the content did change).
	write(t, root, "f", "bbbb", old)
	if got := scan(t, root, first).Entries["f"].Hash; got != hashOf("aaaa") {
		t.Errorf("fast path should have reused the hash")
	}
	if got := scan(t, root, syncdir.Index{}).Entries["f"].Hash; got != hashOf("bbbb") {
		t.Errorf("without a previous scan the file should be hashed")
	}
	write(t, root, "f", "cccc", time.Time{}) // a different time: hashed again.
	recent := scan(t, root, first)
	if recent.Entries["f"].Hash != hashOf("cccc") {
		t.Errorf("changed time should be hashed again")
	}
	// Modified again with the same size and time right after the scan read it: the time was too close to the
	// scan for the hash to be trusted, it is hashed again.
	mtime := recent.Entries["f"].MTime
	write(t, root, "f", "dddd", mtime)
	if got := scan(t, root, recent).Entries["f"].Hash; got != hashOf("dddd") {
		t.Errorf("racy (recent) file should be hashed again")
	}
}

func TestScanErrors(t *testing.T) {
	if _, err := syncdir.Scan(filepath.Join(t.TempDir(), "missing"), syncdir.Index{}, nil); err == nil {
		t.Errorf("expected an error for a missing root")
	}
	if runtime.GOOS == "windows" {
		return
	}
	root := t.TempDir()
	write(t, root, "unreadable", "x", time.Time{})
	write(t, root, "ok", "x", time.Time{})
	_ = os.Chmod(filepath.Join(root, "unreadable"), 0)
	if f, err := os.Open(filepath.Join(root, "unreadable")); err == nil { // eg running as root.
		f.Close()
		t.Skip("can't make an unreadable file")
	}
	idx := scan(t, root, syncdir.Index{})
	if len(idx.Entries) != 1 || idx.Skipped != 1 {
		t.Errorf("unreadable file should be skipped: %+v", idx)
	}
}

func TestScanUnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unreadable directories on windows")
	}
	root := t.TempDir()
	write(t, root, "locked/x", "x", time.Time{})
	write(t, root, "ok", "x", time.Time{})
	locked := filepath.Join(root, "locked")
	_ = os.Chmod(locked, 0)
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil { // eg running as root.
		t.Skip("can't make an unreadable directory")
	}
	idx := scan(t, root, syncdir.Index{})
	if _, ok := idx.Entries["locked"]; !ok || len(idx.Entries) != 2 || idx.Skipped != 1 {
		t.Errorf("unreadable directory should be listed (not its content) and counted: %+v", idx)
	}
}

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"a", "a/b c/d.txt", ".hidden", "é"} {
		if err := syncdir.ValidKey(ok); err != nil {
			t.Errorf("ValidKey(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/abs", "a/", "a//b", ".", "..", "a/../b", "./a", `a\b`, "a\x00b", "a\nb", "é", // NFD.
		strings.Repeat("d/", 64) + "f",
	} {
		if err := syncdir.ValidKey(bad); err == nil {
			t.Errorf("ValidKey(%q) should fail", bad)
		}
	}
}

func TestValidWindowsKey(t *testing.T) {
	for _, ok := range []string{"a", "dir/file.txt", "con2.txt", "com10", "x.y.z", "é"} {
		if err := syncdir.ValidWindowsKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"a:b", "dir/x?", `q"`, "a|b", "<x>", "star*", "dot.", "space ", "CON", "nul.txt",
		"dir/Aux", "com1", "LPT9.log", "dir/con/file",
	} {
		if syncdir.ValidWindowsKey(bad) == nil {
			t.Errorf("%q should be invalid on windows", bad)
		}
	}
}
