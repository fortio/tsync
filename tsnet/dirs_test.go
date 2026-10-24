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

// checkTree checks the received files and their content.
func checkTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	got := readTree(t, root)
	if len(got) != len(files) {
		t.Errorf("%s: got %d files want %d: %v", root, len(got), len(files), got)
	}
	for rel, content := range files {
		if got[rel] != content {
			t.Errorf("%s: %s: content mismatch (%d vs %d bytes)", root, rel, len(got[rel]), len(content))
		}
	}
}

// checkAttrs checks the received modes (minus group/other write) and the directories times.
func checkAttrs(t *testing.T, root string, modes map[string]os.FileMode, dirTime time.Time) {
	t.Helper()
	for _, rel := range []string{"sub/deep", "empty dir", "."} {
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil || !fi.IsDir() || !fi.ModTime().Equal(dirTime) {
			t.Errorf("%s: directory time not kept: %v %v", rel, fi, err)
		}
	}
	if runtime.GOOS == "windows" { // only has a read only attribute.
		return
	}
	for rel, mode := range modes {
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil || fi.Mode().Perm() != mode&^0o022 {
			t.Errorf("%s: mode %v, expected %v (%v)", rel, fi.Mode().Perm(), mode&^0o022, err)
		}
	}
}

// makeTestTree creates the "proj" directory to send: returns its path, files, modes and directories time.
func makeTestTree(t *testing.T) (string, map[string]string, map[string]os.FileMode, time.Time) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "proj")
	files := map[string]string{
		"a.txt":         "hello",
		"empty":         "",
		"sub/b.txt":     "bee",
		"sub/deep/c.go": string(bytes.Repeat([]byte("c"), 3*tsnet.FileChunkSize+7)),
		"sub/deep/d":    "dee",
		"e1":            "1", "e2": "2", "e3": "3", "e4": "4", "e5": "5", // more files than dirParallel.
		".hidden/.env": "kept hidden",
		"run.sh":       "#!/bin/sh\n",
	}
	writeTree(t, src, files)
	if runtime.GOOS != "windows" {
		_ = os.Symlink("a.txt", filepath.Join(src, "link")) // skipped.
	}
	// Modes and times are kept (except group/other write), including for (empty) directories.
	dirTime := time.Date(2019, 5, 6, 7, 8, 9, 0, time.UTC)
	modes := map[string]os.FileMode{"run.sh": 0o775, "a.txt": 0o640, "sub/deep": 0o750, "empty dir": 0o700}
	if err := os.Mkdir(filepath.Join(src, "empty dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, mode := range modes {
		if err := os.Chmod(filepath.Join(src, rel), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"sub/deep", "empty dir", "."} {
		if err := os.Chtimes(filepath.Join(src, rel), dirTime, dirTime); err != nil {
			t.Fatal(err)
		}
	}
	return src, files, modes, dirTime
}

func TestDirectoryTransfer(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirB := t.TempDir()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: dirB})
	src, files, modes, dirTime := makeTestTree(t)
	// 2nd time through a symlink with the same name (the directory itself is a link).
	offered := []string{src, src}
	if runtime.GOOS != "windows" {
		offered[1] = filepath.Join(t.TempDir(), "proj")
		if err := os.Symlink(src, offered[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"proj", "proj-1"} {
		before := len(serverB.Transfers())
		off, err := serverA.OfferPath(offered[i])
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
		checkTree(t, done.Path, files)
		fi, err := os.Stat(filepath.Join(done.Path, "sub", "b.txt"))
		if err != nil || fi.ModTime().Year() != 2021 {
			t.Errorf("mtime not preserved: %v %v", fi, err)
		}
		checkAttrs(t, done.Path, modes, dirTime)
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

// A file of an offered directory replaced by a symlink to outside of it (same size and time) isn't sent.
func TestDirectorySymlinkSwap(t *testing.T) {
	NoMCastOnMacInCI(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirB := t.TempDir()
	serverA, serverB, peerA := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA"}, tsnet.Config{Name: "HostB", FileDir: dirB})
	src := filepath.Join(t.TempDir(), "swap")
	writeTree(t, src, map[string]string{"a": "aaaa"})
	secret := filepath.Join(t.TempDir(), "secret")
	writeTree(t, filepath.Dir(secret), map[string]string{"secret": "SSSS"}) // same size and mtime.
	if _, err := serverA.OfferPath(src); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, "B got the offer", func() bool { return len(serverB.Transfers()) == 1 })
	if err := os.Remove(filepath.Join(src, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "a")); err != nil {
		t.Fatal(err)
	}
	if err := serverB.AcceptFile(peerA, serverB.Transfers()[0].ID); err != nil {
		t.Fatal(err)
	}
	waitTransfer(ctx, t, serverB, true, "swap", tsnet.TransferFailed)
	waitTransfer(ctx, t, serverA, false, "swap", tsnet.TransferFailed)
	if got := readTree(t, filepath.Join(dirB, "swap")); len(got) != 0 {
		t.Errorf("nothing should have been received: %v", got)
	}
}
