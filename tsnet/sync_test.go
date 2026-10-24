package tsnet_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tsnet"
)

// syncStatus waits for srv's status of folder with a peer (ready) matching ok, returns it.
func syncStatus(ctx context.Context, t *testing.T, srv *tsnet.Server, folder string,
	ok func(tsnet.SyncStatus) bool,
) tsnet.SyncStatus {
	t.Helper()
	var res tsnet.SyncStatus
	waitFor(ctx, t, srv.Name+" sync "+folder, func() bool {
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

func TestSyncDryRun(t *testing.T) {
	NoMCastOnMacInCI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dirA, dirB, solo := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, dirA, map[string]string{"same.txt": "same", "onlyA.txt": "a", "both.txt": "from A"})
	writeTree(t, dirB, map[string]string{"same.txt": "same", "onlyB.txt": "bb", "both.txt": "from B", ".git/x": "hidden"})
	writeTree(t, solo, map[string]string{"x": "x"})
	serverA, serverB, _ := linkedPair(ctx, t,
		tsnet.Config{Name: "HostA", Sync: []tsnet.SyncFolder{{Name: "docs", Path: dirA}, {Name: "solo", Path: solo}}},
		tsnet.Config{Name: "HostB", Sync: []tsnet.SyncFolder{{Name: "docs", Path: dirB}}, SyncRescan: 100 * time.Millisecond})
	ready := func(st tsnet.SyncStatus) bool { return st.Ready }
	stA := syncStatus(ctx, t, serverA, "docs", ready)
	if c := stA.Summary.Counts; stA.Peer.Name != "HostB" || c[syncdir.Pull] != 1 || stA.Summary.PullBytes != 2 ||
		c[syncdir.PeerPulls] != 1 || c[syncdir.Conflict] != 1 || c[syncdir.InSync] != 1 {
		t.Errorf("unexpected A status %+v", stA)
	}
	if len(stA.Summary.Conflicts) != 1 || stA.Summary.Conflicts[0] != "both.txt" {
		t.Errorf("unexpected conflicts %v", stA.Summary.Conflicts)
	}
	stB := syncStatus(ctx, t, serverB, "docs", ready)
	if c := stB.Summary.Counts; c[syncdir.Pull] != 1 || stB.Summary.PullBytes != 1 || c[syncdir.PeerPulls] != 1 ||
		c[syncdir.Conflict] != 1 || stB.Files != 3 { // .git and .git/x are ignored.
		t.Errorf("unexpected B status %+v", stB)
	}
	// A folder only we have: no peer, and B never hears about its content.
	soloSt := syncStatus(ctx, t, serverA, "solo", func(st tsnet.SyncStatus) bool { return st.Scanned })
	if soloSt.Peer != (tsnet.Peer{}) || soloSt.Files != 1 {
		t.Errorf("unexpected solo status %+v", soloSt)
	}
	for _, st := range serverB.SyncStatus() {
		if st.Folder != "docs" {
			t.Errorf("B shouldn't have %+v", st)
		}
	}
	// B's rescan sees a new file, A's plan is updated.
	if err := os.WriteFile(filepath.Join(dirB, "later.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncStatus(ctx, t, serverA, "docs", func(st tsnet.SyncStatus) bool {
		return st.Ready && st.Summary.Counts[syncdir.Pull] == 2 && st.Summary.PullBytes == 7
	})
	// Nothing was written (dry run).
	if _, err := os.Stat(filepath.Join(dirA, "onlyB.txt")); err == nil {
		t.Errorf("dry run shouldn't write anything")
	}
}
