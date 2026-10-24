package tsnet_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/tsnet"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	mtime := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mtime, mtime)
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	res := make(map[string]string)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		res[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDirectoryTransfer(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirB := t.TempDir()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: dirB})
	src := filepath.Join(t.TempDir(), "proj")
	files := map[string]string{
		"a.txt":         "hello",
		"empty":         "",
		"sub/b.txt":     "bee",
		"sub/deep/c.go": string(bytes.Repeat([]byte("c"), 3*tsnet.FileChunkSize+7)),
		"sub/deep/d":    "dee",
		"e1":            "1", "e2": "2", "e3": "3", "e4": "4", "e5": "5", // more files than dirParallel.
	}
	writeTree(t, src, files)
	if runtime.GOOS != "windows" {
		_ = os.Symlink("a.txt", filepath.Join(src, "link")) // skipped.
	}
	for i, want := range []string{"proj", "proj-1"} {
		before := len(serverB.Transfers())
		off, err := serverA.OfferPath(src)
		if err != nil || off.Peers != 1 || off.Files != len(files) {
			t.Fatalf("OfferPath: %+v %v", off, err)
		}
		var offered tsnet.Transfer
		waitFor(ctx, t, "B got the offer", func() bool {
			trs := serverB.Transfers()
			if len(trs) <= before {
				return false
			}
			offered = trs[len(trs)-1]
			return true
		})
		if !offered.Dir || offered.Files != len(files) || offered.State != tsnet.TransferOffered {
			t.Fatalf("unexpected offer %+v", offered)
		}
		if err = serverB.AcceptFile(peerA, offered.ID); err != nil {
			t.Fatal(err)
		}
		if err = serverB.AcceptFile(peerA, offered.ID); err == nil {
			t.Errorf("accepting twice should fail")
		}
		done := waitTransfer(ctx, t, serverB, true, want, tsnet.TransferDone)
		waitTransfer(ctx, t, serverA, false, "proj", tsnet.TransferDone)
		if done.Path != filepath.Join(dirB, want) || done.Done != done.Size {
			t.Errorf("unexpected done transfer %+v", done)
		}
		got := readTree(t, done.Path)
		if len(got) != len(files) {
			t.Errorf("run %d: got %d files want %d: %v", i, len(got), len(files), got)
		}
		for rel, content := range files {
			if got[rel] != content {
				t.Errorf("run %d: %s: content mismatch (%d vs %d bytes)", i, rel, len(got[rel]), len(content))
			}
		}
		fi, err := os.Stat(filepath.Join(done.Path, "sub", "b.txt"))
		if err != nil || fi.ModTime().Year() != 2021 {
			t.Errorf("mtime not preserved: %v %v", fi, err)
		}
		checkNoPartial(t, done.Path)
		checkNoPartial(t, filepath.Join(done.Path, "sub", "deep"))
		// The offer from A is a new one each time (a new id).
		waitFor(ctx, t, "A's transfer is done", func() bool {
			for _, tr := range serverA.Transfers() {
				if tr.State == tsnet.TransferActive {
					return false
				}
			}
			return true
		})
	}
	if _, err := serverA.OfferPath(t.TempDir()); err == nil {
		t.Errorf("expected an error offering an empty directory")
	}
}

func TestDirectoryFileChanged(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: t.TempDir()})
	src := filepath.Join(t.TempDir(), "chg")
	writeTree(t, src, map[string]string{"a": "aaa", "b/c": "ccc"})
	if _, err := serverA.OfferPath(src); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, "B got the offer", func() bool { return len(serverB.Transfers()) == 1 })
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("changed after the offer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := serverB.AcceptFile(peerA, serverB.Transfers()[0].ID); err != nil {
		t.Fatal(err)
	}
	failed := waitTransfer(ctx, t, serverB, true, "chg", tsnet.TransferFailed)
	if !strings.Contains(failed.Err, "changed") {
		t.Errorf("unexpected error %q", failed.Err)
	}
	waitTransfer(ctx, t, serverA, false, "chg", tsnet.TransferFailed)
}
