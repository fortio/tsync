package tsnet_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

// syncStatus waits for srv's status of folder matching ok, returns it.
func syncStatus(ctx context.Context, t *testing.T, srv *tsnet.Server, folder, what string,
	ok func(tsnet.SyncStatus) bool,
) tsnet.SyncStatus {
	t.Helper()
	var res tsnet.SyncStatus
	waitFor(ctx, t, srv.Name+" sync "+folder+": "+what, func() bool {
		for _, st := range srv.SyncStatus() {
			if st.Folder == folder && ok(st) {
				res = st
				return true
			}
		}
		return false
	})
	return res
}

// inSync is a ready status with nothing left to do.
func inSync(st tsnet.SyncStatus) bool {
	return st.Ready && st.Getting == 0 && st.Summary.Pending() == 0
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestSync(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dirA, dirB, solo := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, dirA, map[string]string{
		"same.txt": "same", "onlyA.txt": "a", "both.txt": "from A", "sub/deep/x.txt": "deep", "run.sh": "#!/bin/sh\n",
	})
	if err := os.Mkdir(filepath.Join(dirA, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(filepath.Join(dirA, "run.sh"), 0o755)
	}
	writeTree(t, dirB, map[string]string{"same.txt": "same", "onlyB.txt": "bb", "both.txt": "from B", ".git/x": "hidden"})
	writeTree(t, solo, map[string]string{"x": "x"})
	storeA, storeB := &tcrypto.Storage{Dir: t.TempDir()}, &tcrypto.Storage{Dir: t.TempDir()}
	common := tsnet.Config{SyncRescan: 100 * time.Millisecond, SyncKeep: 2}
	cfgA, cfgB := common, common
	cfgA.Name, cfgA.Storage = "HostA", storeA
	cfgA.Sync = []tsnet.SyncFolder{{Name: "docs", Path: dirA}, {Name: "solo", Path: solo}}
	cfgB.Name, cfgB.Storage = "HostB", storeB
	cfgB.Sync = []tsnet.SyncFolder{{Name: "docs", Path: dirB}}
	serverA, serverB, peerA := linkedPair(ctx, t, cfgA, cfgB)
	peerB := waitForPeer(ctx, t, serverA, "HostB")
	// Each side gets what the other has; both.txt (new on both sides, different) is a conflict, resolved with a
	// conflict copy (same time: picked by content) on both sides.
	withCopy := func(st tsnet.SyncStatus) bool { return inSync(st) && st.ConflictCopies == 1 }
	stA := syncStatus(ctx, t, serverA, "docs", "in sync with a conflict copy", withCopy)
	syncStatus(ctx, t, serverB, "docs", "in sync with a conflict copy", withCopy)
	copies, _ := filepath.Glob(filepath.Join(dirA, "both"+syncdir.ConflictCopyMarker+"*-20210203-040506.txt"))
	if len(copies) != 1 {
		t.Fatalf("expected a conflict copy, got %v", copies)
	}
	copyName := filepath.Base(copies[0])
	kept, saved := content(t, filepath.Join(dirA, "both.txt")), content(t, copies[0])
	if kept == saved || (kept != "from A" && kept != "from B") || (saved != "from A" && saved != "from B") ||
		content(t, filepath.Join(dirB, "both.txt")) != kept || content(t, filepath.Join(dirB, copyName)) != saved {
		t.Errorf("unexpected conflict resolution: kept %q, saved %q as %s", kept, saved, copyName)
	}
	conflictsB := filepath.Join(storeB.Dir, "sync", "docs", tsnet.ConflictsFileName)
	if stA.ConflictsFile != filepath.Join(storeA.Dir, "sync", "docs", tsnet.ConflictsFileName) {
		t.Errorf("unexpected conflicts file %q", stA.ConflictsFile)
	}
	waitFor(ctx, t, "conflict copy listed", func() bool {
		return strings.Contains(content(t, conflictsB), "\nconflict copy\t"+copyName+"\there: 6 b, ")
	})
	for p, want := range map[string]string{
		filepath.Join(dirA, "onlyB.txt"): "bb", filepath.Join(dirB, "onlyA.txt"): "a",
		filepath.Join(dirB, "sub", "deep", "x.txt"): "deep",
	} {
		if got := content(t, p); got != want {
			t.Errorf("%s: got %q, want %q", p, got, want)
		}
	}
	for _, f := range []string{
		filepath.Join(storeA.Dir, "sync", "docs", peerB.PublicKey+".base"),
		filepath.Join(storeB.Dir, "sync", "docs", peerA.PublicKey+".base"),
	} {
		waitFor(ctx, t, "base saved "+f, func() bool { // saved by the folder's goroutine, soon after syncing.
			b, err := syncdir.LoadBase(f)
			return err == nil && len(b) == 10 // with both.txt and its conflict copy.
		})
	}
	// Changed on A: B gets it, keeping its previous version in OldDir.
	writeTree(t, dirA, map[string]string{"onlyA.txt": "a v2"}) // older time than when it was synced: still a change.
	syncStatus(ctx, t, serverB, "docs", "got the change", func(st tsnet.SyncStatus) bool {
		return content(t, filepath.Join(dirB, "onlyA.txt")) == "a v2" && withCopy(st)
	})
	old, _ := filepath.Glob(filepath.Join(dirB, syncdir.OldDir, "onlyA.txt~*"))
	if len(old) != 1 || content(t, old[0]) != "a" {
		t.Errorf("previous version not kept: %v", old)
	}
	// The conflict copy, once reviewed, is deleted on one side: deleted on the other too.
	if err := os.Remove(filepath.Join(dirB, copyName)); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverA, "docs", "conflict copy deleted", func(st tsnet.SyncStatus) bool {
		_, err := os.Stat(filepath.Join(dirA, copyName))
		return os.IsNotExist(err) && inSync(st) && st.ConflictCopies == 0
	})
	waitFor(ctx, t, "conflicts list removed", func() bool {
		_, err := os.Stat(conflictsB)
		return os.IsNotExist(err)
	})
	// A folder only we have: no peer, and B never hears about its content.
	soloSt := syncStatus(ctx, t, serverA, "solo", "scanned", func(st tsnet.SyncStatus) bool { return st.Scanned })
	if soloSt.Peer != (tsnet.Peer{}) || soloSt.Files != 1 {
		t.Errorf("unexpected solo status %+v", soloSt)
	}
	for _, st := range serverB.SyncStatus() {
		if st.Folder != "docs" {
			t.Errorf("B shouldn't have %+v", st)
		}
	}
}

func TestSyncDeletion(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dirA, dirB := t.TempDir(), t.TempDir()
	files := map[string]string{"same.txt": "same", "sub/deep/x.txt": "deep", "keep.txt": "keep", "mv/a": "a", "mv/b": "b"}
	writeTree(t, dirA, files)
	writeTree(t, dirB, files)
	common := tsnet.Config{SyncRescan: 100 * time.Millisecond, SyncKeep: 2}
	cfgA, cfgB := common, common
	cfgA.Name, cfgA.Sync = "HostA", []tsnet.SyncFolder{{Name: "docs", Path: dirA}}
	cfgB.Name, cfgB.Sync = "HostB", []tsnet.SyncFolder{{Name: "docs", Path: dirB}}
	serverA, serverB, _ := linkedPair(ctx, t, cfgA, cfgB)
	syncStatus(ctx, t, serverA, "docs", "in sync", inSync)
	syncStatus(ctx, t, serverB, "docs", "in sync", inSync)
	var old []string
	// A deletion is propagated: B moves its copy to OldDir.
	if err := os.Remove(filepath.Join(dirA, "same.txt")); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverB, "docs", "deletion propagated", func(st tsnet.SyncStatus) bool {
		_, err := os.Stat(filepath.Join(dirB, "same.txt"))
		return os.IsNotExist(err) && inSync(st)
	})
	syncStatus(ctx, t, serverA, "docs", "in sync", inSync)
	old, _ = filepath.Glob(filepath.Join(dirB, syncdir.OldDir, "same.txt~*"))
	if len(old) != 1 || content(t, old[0]) != "same" {
		t.Errorf("deleted file not kept: %v", old)
	}
	// A deleted directory too (its files first).
	if err := os.RemoveAll(filepath.Join(dirB, "sub")); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverA, "docs", "directory deletion propagated", func(st tsnet.SyncStatus) bool {
		_, err := os.Stat(filepath.Join(dirA, "sub"))
		return os.IsNotExist(err) && inSync(st)
	})
	old, _ = filepath.Glob(filepath.Join(dirA, syncdir.OldDir, "sub", "deep", "x.txt~*"))
	if len(old) != 1 || content(t, old[0]) != "deep" {
		t.Errorf("deleted directory's file not kept: %v", old)
	}
	// A moved directory is moved on the other side (renamed, not downloaded nor kept in OldDir).
	if err := os.Rename(filepath.Join(dirA, "mv"), filepath.Join(dirA, "moved")); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverB, "docs", "moved", func(st tsnet.SyncStatus) bool {
		_, err := os.Stat(filepath.Join(dirB, "mv"))
		return os.IsNotExist(err) && content(t, filepath.Join(dirB, "moved", "b")) == "b" && inSync(st)
	})
	if moved, _ := filepath.Glob(filepath.Join(dirB, syncdir.OldDir, "mv", "*")); len(moved) != 0 {
		t.Errorf("moved files shouldn't be in %s: %v", syncdir.OldDir, moved)
	}
	time.Sleep(500 * time.Millisecond) // a few rescans (and replans) later, nothing comes back:
	for _, p := range []string{filepath.Join(dirA, "same.txt"), filepath.Join(dirB, "same.txt"), filepath.Join(dirA, "sub")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s shouldn't come back", p)
		}
	}
}
