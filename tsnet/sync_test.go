package tsnet_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

// onlyConflicts is a ready status with nothing left to do but n conflicts (and the deletions not propagated).
func onlyConflicts(n, deleted int) func(tsnet.SyncStatus) bool {
	return func(st tsnet.SyncStatus) bool {
		c := st.Summary.Counts
		return st.Ready && st.Getting == 0 && c[syncdir.Conflict] == n &&
			st.Summary.Pending() == n+deleted && c[syncdir.DeletedHere]+c[syncdir.DeletedThere] == deleted
	}
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
	// Each side gets what the other has; both.txt (new on both sides, different) is a conflict.
	stA := syncStatus(ctx, t, serverA, "docs", "only the conflict left", onlyConflicts(1, 0))
	syncStatus(ctx, t, serverB, "docs", "only the conflict left", onlyConflicts(1, 0))
	if stA.Summary.Conflicts[0] != "both.txt" {
		t.Errorf("unexpected conflicts %v", stA.Summary.Conflicts)
	}
	for p, want := range map[string]string{
		filepath.Join(dirA, "onlyB.txt"): "bb", filepath.Join(dirB, "onlyA.txt"): "a",
		filepath.Join(dirB, "sub", "deep", "x.txt"): "deep", filepath.Join(dirA, "both.txt"): "from A",
		filepath.Join(dirB, "both.txt"): "from B",
	} {
		if got := content(t, p); got != want {
			t.Errorf("%s: got %q, want %q", p, got, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dirB, "empty")); err != nil || !fi.IsDir() {
		t.Errorf("empty directory not created: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dirB, "run.sh")); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o755) {
		t.Errorf("mode not kept: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dirA, ".git")); err == nil {
		t.Errorf("hidden files shouldn't be synced by default")
	}
	for _, f := range []string{
		filepath.Join(storeA.Dir, "sync", "docs", peerB.PublicKey+".base"),
		filepath.Join(storeB.Dir, "sync", "docs", peerA.PublicKey+".base"),
	} {
		waitFor(ctx, t, "base saved "+f, func() bool { // saved by the folder's goroutine, soon after syncing.
			b, err := syncdir.LoadBase(f)
			return err == nil && len(b) == 8 // all but the conflict.
		})
	}
	// Changed on A: B gets it, keeping its previous version in OldDir.
	writeTree(t, dirA, map[string]string{"onlyA.txt": "a v2"}) // older time than when it was synced: still a change.
	syncStatus(ctx, t, serverB, "docs", "got the change", func(st tsnet.SyncStatus) bool {
		return content(t, filepath.Join(dirB, "onlyA.txt")) == "a v2" && onlyConflicts(1, 0)(st)
	})
	old, _ := filepath.Glob(filepath.Join(dirB, syncdir.OldDir, "onlyA.txt~*"))
	if len(old) != 1 || content(t, old[0]) != "a" {
		t.Errorf("previous version not kept: %v", old)
	}
	// The conflict is resolved by hand by deleting one side: the other version wins.
	if err := os.Remove(filepath.Join(dirB, "both.txt")); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverB, "docs", "conflict resolved", func(st tsnet.SyncStatus) bool {
		return content(t, filepath.Join(dirB, "both.txt")) == "from A" && onlyConflicts(0, 0)(st)
	})
	// A deletion isn't propagated, only shown.
	if err := os.Remove(filepath.Join(dirA, "same.txt")); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverB, "docs", "deleted there", func(st tsnet.SyncStatus) bool {
		return st.Summary.Counts[syncdir.DeletedThere] == 1 && onlyConflicts(0, 1)(st)
	})
	syncStatus(ctx, t, serverA, "docs", "deleted here", func(st tsnet.SyncStatus) bool {
		return st.Summary.Counts[syncdir.DeletedHere] == 1 && onlyConflicts(0, 1)(st)
	})
	if content(t, filepath.Join(dirB, "same.txt")) != "same" {
		t.Errorf("deletion shouldn't be propagated")
	}
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
