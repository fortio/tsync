package tsnet

import (
	"path/filepath"
	"testing"
	"time"

	"fortio.org/tsync/syncdir"
	"github.com/fsnotify/fsnotify"
)

func TestSyncWatchEvents(t *testing.T) {
	root := t.TempDir()
	sw := &syncWatch{root: root, watched: map[string]bool{"": true, "d": true, "d/e": true}, events: map[string]bool{}}
	sw.reset()
	if sw.quietIn() != 0 {
		t.Errorf("nothing notified yet")
	}
	sw.event(fsnotify.Event{Name: filepath.Join(root, "d", "f.txt"), Op: fsnotify.Write})
	sw.event(fsnotify.Event{Name: filepath.Join(root, "d", "e"), Op: fsnotify.Remove}) // a watched directory.
	sw.event(fsnotify.Event{Name: filepath.Join(root, "top"), Op: fsnotify.Create})
	sw.event(fsnotify.Event{Name: root, Op: fsnotify.Chmod}) // the root itself.
	for _, d := range []string{"d", "d/e", ""} {
		if !sw.dirty[d] || !sw.events[d] {
			t.Errorf("%q should be dirty", d)
		}
	}
	if len(sw.dirty) != 3 || !sw.force["d/f.txt"] || len(sw.force) != 1 || sw.full {
		t.Errorf("unexpected %v %v %v", sw.dirty, sw.force, sw.full)
	}
	if q := sw.quietIn(); q <= 0 || q > syncQuiet {
		t.Errorf("unexpected quiet %v", q)
	}
	sw.event(fsnotify.Event{Name: filepath.Join(root, syncdir.IgnoreFile), Op: fsnotify.Write})
	if !sw.full {
		t.Errorf("a changed %s should need a full scan", syncdir.IgnoreFile)
	}
	sw.since = time.Now().Add(-syncQuietMax) // notifications keep coming: scanned anyway.
	if q := sw.quietIn(); q > time.Millisecond {
		t.Errorf("unexpected quiet %v after the max", q)
	}
}

func TestSyncWatchMissed(t *testing.T) {
	e := func(p string, c byte) syncdir.Entry { return syncdir.Entry{Path: p, Size: 1, Hash: syncdir.Hash{c}} }
	old := syncdir.Index{Entries: map[string]syncdir.Entry{"a/x": e("a/x", 1), "b/y": e("b/y", 1), "c/z": e("c/z", 1)}}
	idx := syncdir.Index{Entries: map[string]syncdir.Entry{"a/x": e("a/x", 2), "b/y": e("b/y", 2), "d/new": e("d/new", 1)}}
	sw := &syncWatch{events: map[string]bool{"a": true, "d": true}}
	got := sw.missed(old, idx)
	if len(got) != 2 || got[0] != "b/y" || got[1] != "c/z" {
		t.Errorf("expected b/y (changed) and c/z (removed) missed, got %q", got)
	}
}
