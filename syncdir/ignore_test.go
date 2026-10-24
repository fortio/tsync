package syncdir_test

import (
	"os"
	"path/filepath"
	"testing"

	"fortio.org/tsync/syncdir"
)

type match struct {
	key  string
	dir  bool
	want bool
}

func checkMatches(t *testing.T, name string, ig *syncdir.Ignore, cases []match) {
	t.Helper()
	for _, c := range cases {
		if got := ig.Match(c.key, c.dir); got != c.want {
			t.Errorf("%s: Match(%q, dir %v) = %v, want %v", name, c.key, c.dir, got, c.want)
		}
	}
}

func TestDefaultIgnore(t *testing.T) {
	checkMatches(t, "default", syncdir.DefaultIgnore(), []match{
		{"file.txt", false, false},
		{"sub/file.txt", false, false},
		{".git", true, true},
		{".git/config", false, true},
		{"a/.env", false, true},
		{".tsyncignore", false, false}, // so peers get it even before they have one.
		{"a/.tsyncignore", false, true},
		{".tsync-trash", true, true},
		{"x.tsync-partial", false, true},
	})
}

func TestParseIgnore(t *testing.T) {
	ig, err := syncdir.ParseIgnore(`# comment

build/
*.log
/top.txt
a/**/b
**/tmp
.git/
docs/*.md
e` + "́" + `*
`)
	if err != nil {
		t.Fatal(err)
	}
	checkMatches(t, "custom", ig, []match{
		{"build", true, true},
		{"build", false, false}, // directories only.
		{"x/build/y", false, true},
		{"x/y.log", false, true},
		{"top.txt", false, true},
		{"sub/top.txt", false, false}, // anchored.
		{"a/b", true, true},
		{"a/x/y/b", false, true},
		{"a/b/c", false, true}, // in an ignored directory.
		{"tmp", false, true},
		{"x/tmp/z", false, true},
		{".git/config", false, true},
		{".env", false, false}, // only what is listed is ignored.
		{".tsyncignore", false, false},
		{"docs/a.md", false, true},
		{"docs/sub/a.md", false, false},
		{"été", false, true}, // NFC normalized pattern.
		{".tsync-trash/x", false, true},
		{"a.tsync-partial", false, true},
	})
	self, err := syncdir.ParseIgnore(".tsyncignore\n")
	if err != nil || !self.Match(".tsyncignore", false) {
		t.Errorf("an ignore file listing itself should be ignored: %v", err)
	}
	for _, bad := range []string{"!keep", "[", "/", "a//b"} {
		if _, err := syncdir.ParseIgnore("ok\n" + bad + "\n"); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestLoadIgnore(t *testing.T) {
	root := t.TempDir()
	ig, err := syncdir.LoadIgnore(root)
	if err != nil || !ig.Match(".hidden", false) {
		t.Errorf("expected the default rules: %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, syncdir.IgnoreFile), []byte("*.o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig, err = syncdir.LoadIgnore(root)
	if err != nil || ig.Match(".hidden", false) || !ig.Match("x.o", false) {
		t.Errorf("expected the .tsyncignore rules: %v", err)
	}
	idx, err := syncdir.Scan(root, syncdir.Index{}, ig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Entries[syncdir.IgnoreFile]; !ok || len(idx.Entries) != 1 {
		t.Errorf("unexpected scan %+v", idx.Entries)
	}
}
