package syncdir_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"fortio.org/tsync/syncdir"
)

func TestBaseSaveLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sync", "folder", "peer.base")
	b, err := syncdir.LoadBase(file)
	if err != nil || len(b) != 0 {
		t.Fatalf("missing file should be an empty base: %v %v", b, err)
	}
	f := syncdir.Entry{Path: `a b/"q" \é.txt`, Mode: 0o644, Size: 3, Hash: hashOf("abc")}
	b.Record(f)
	b.Record(syncdir.Entry{Path: "dir", Dir: true, Mode: 0o700})
	if err = b.Save(file); err != nil {
		t.Fatal(err)
	}
	got, err := syncdir.LoadBase(file)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, b) {
		t.Errorf("got %+v, want %+v", got, b)
	}
	if got[f.Path].Hash != f.Hash || !got["dir"].Dir || got["dir"].Mode != 0o700 {
		t.Errorf("unexpected entries %+v", got)
	}
	// Saved again (replaced).
	delete(b, "dir")
	if err = b.Save(file); err != nil {
		t.Fatal(err)
	}
	if got, err = syncdir.LoadBase(file); err != nil || len(got) != 1 {
		t.Errorf("unexpected base after saving again: %+v %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestLoadBaseErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "peer.base")
	for _, content := range []string{
		"something else\n",
		"tsyncbase1\nx 644 - \"p\"\n",
		"tsyncbase1\nf 644 nothex \"p\"\n",
		"tsyncbase1\nf 1777 " + hashOf("").String() + " \"p\"\n",
		"tsyncbase1\nd 755 " + hashOf("").String() + " \"p\"\n",
		"tsyncbase1\nd 755 - p\n",
		"tsyncbase1\nd 755 - \"\"\n",
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, err := syncdir.LoadBase(file); err == nil {
			t.Errorf("expected an error for %q, got %+v", content, b)
		}
	}
}
