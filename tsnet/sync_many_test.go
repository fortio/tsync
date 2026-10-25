package tsnet_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

// TestSyncMany times the initial sync of SYNC_MANY small files (eg SYNC_MANY=20000 go test -run TestSyncMany -v
// ./tsnet), not run otherwise: to check it stays linear (100k files took 22s on a M5 Max, ~4.5k files/s).
func TestSyncMany(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("SYNC_MANY"))
	if n == 0 {
		t.Skip("SYNC_MANY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dirA, dirB := t.TempDir(), t.TempDir()
	old := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		p := filepath.Join(dirA, "d"+strconv.Itoa(i%100), "f"+strconv.Itoa(i))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(strconv.Itoa(i)), 0o644)
		_ = os.Chtimes(p, old, old)
	}
	common := tsnet.Config{SyncKeep: 1}
	cfgA, cfgB := common, common
	cfgA.Name, cfgA.Sync, cfgA.Storage = "HostA", []tsnet.SyncFolder{{Name: "docs", Path: dirA}}, &tcrypto.Storage{Dir: t.TempDir()}
	cfgB.Name, cfgB.Sync, cfgB.Storage = "HostB", []tsnet.SyncFolder{{Name: "docs", Path: dirB}}, &tcrypto.Storage{Dir: t.TempDir()}
	serverA, serverB, _ := linkedPair(ctx, t, cfgA, cfgB)
	start := time.Now()
	syncStatus(ctx, t, serverB, "docs", "all got", func(st tsnet.SyncStatus) bool {
		return st.Ready && st.Getting == 0 && st.Summary.Pending() == 0 && st.Files == n+100
	})
	t.Logf("B got %d files in %v", n, time.Since(start))
	syncStatus(ctx, t, serverA, "docs", "A in sync", func(st tsnet.SyncStatus) bool {
		return st.Ready && st.Summary.Pending() == 0 && st.Summary.Counts[syncdir.InSync] == n+100
	})
	t.Logf("both in sync after %v", time.Since(start))
}
