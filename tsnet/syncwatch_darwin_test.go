//go:build cgo

package tsnet

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestFSEventsWatcher(t *testing.T) {
	parent := t.TempDir() // under /var, notified as /private/var.
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := newWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err = w.Add(root); err != nil {
		t.Fatal(err)
	}
	if err = w.Add(filepath.Join(root, "other")); err != nil { // already in the tree.
		t.Errorf("second Add: %v", err)
	}
	f := filepath.Join(root, "f.txt")
	if err = os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(10 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-w.Events:
			got = ev.Name == f && ev.Has(fsnotify.Create)
		case e := <-w.Errors:
			t.Fatalf("unexpected error %v", e)
		case <-timeout:
			t.Fatalf("no event for %s", f)
		}
	}
	if err = os.Rename(root, root+"2"); err != nil {
		t.Fatal(err)
	}
	for stopped := false; !stopped; { // an error, then the notifications stop.
		select {
		case _, ok := <-w.Events:
			stopped = !ok
		case <-w.Errors:
		case <-timeout:
			t.Fatalf("notifications not stopped after the root was moved")
		}
	}
	w.Close()
	w.Close() // no op.
}
