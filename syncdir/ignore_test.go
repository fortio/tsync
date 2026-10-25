package syncdir_test

import (
	"os"
	"os/exec"
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
		{".tsync-old", true, true},
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
		{".tsync-old/x", false, true},
		{"a.tsync-partial", false, true},
	})
	self, err := syncdir.ParseIgnore(".tsyncignore\n")
	if err != nil || !self.Match(".tsyncignore", false) {
		t.Errorf("an ignore file listing itself should be ignored: %v", err)
	}
	for _, bad := range []string{"!", "!/", "[", "/", "a//b"} {
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

func TestIgnoreNegation(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		cases       []match
	}{
		{"all but a file at the root", "*\n!keep.txt\n", []match{
			{"keep.txt", false, false},
			{"other.txt", false, true},
			{"sub", true, true},
			{"sub/keep.txt", false, true}, // its directory is ignored: can't be re-included.
			{".tsyncignore", false, true}, // listed by *.
		}},
		{"all but go files (and directories)", "*\n!*/\n!*.go\n!.tsyncignore\n", []match{
			{"a.go", false, false},
			{"sub", true, false},
			{"sub/b.go", false, false},
			{"sub/c.txt", false, true},
			{"sub/deep/d.go", false, false},
			{".tsyncignore", false, false},
		}},
		{"all logs but one", "*.log\n!important.log\n", []match{
			{"x.log", false, true},
			{"important.log", false, false},
			{"sub/important.log", false, false},
			{"sub/x.log", false, true},
		}},
		{"order matters: the last match wins", "!keep.txt\n*\n", []match{
			{"keep.txt", false, true},
		}},
		{"not inside an ignored directory", "logs/\n!logs/keep.log\n", []match{
			{"logs/keep.log", false, true},
			{"logs", true, true},
		}},
		{"re-included directory", "/build/\n!/build/\n", []match{
			{"build/x", false, false},
		}},
	} {
		ig, err := syncdir.ParseIgnore(tc.rules)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		checkMatches(t, tc.name, ig, tc.cases)
	}
}

// Same answers as git for the rules both support (.gitignore with the same lines, `git check-ignore`).
func TestIgnoreLikeGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	paths := make([]match, 0, 25)
	for _, d := range []string{"sub", "logs", "build", "a/b"} {
		paths = append(paths, match{key: d, dir: true})
	}
	for _, f := range []string{
		"keep.txt", "other.txt", "a.go", "x.log", "important.log", "top.txt", "sub/keep.txt", "sub/b.go", "sub/c.txt",
		"sub/x.log", "sub/important.log", "sub/top.txt", "sub/deep/d.go", "logs/keep.log", "build/x", "a/x/y/b",
		"a/b/c", "tmp", "x/tmp/z", "docs/a.md", "docs/sub/a.md",
	} {
		paths = append(paths, match{key: f})
	}
	for _, rules := range []string{
		"*\n!keep.txt\n",
		"*\n!*/\n!*.go\n",
		"*.log\n!important.log\n",
		"!keep.txt\n*\n",
		"logs/\n!logs/keep.log\n",
		"/build/\n!/build/\n",
		"# comment\n\nbuild/\n*.log\n/top.txt\na/**/b\n**/tmp\ndocs/*.md\n",
		"sub/\n!sub/\n*.txt\n!keep.txt\n",
	} {
		dir := t.TempDir()
		if err = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(rules), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, gerr := exec.Command(gitPath, "-C", dir, "init", "-q").CombinedOutput(); gerr != nil {
			t.Fatalf("git init: %v %s", gerr, out)
		}
		for _, p := range paths { // git only applies directory rules to directories that exist.
			full := filepath.Join(dir, filepath.FromSlash(p.key))
			if p.dir {
				err = os.MkdirAll(full, 0o755)
			} else if err = os.MkdirAll(filepath.Dir(full), 0o755); err == nil {
				err = os.WriteFile(full, nil, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		ig, perr := syncdir.ParseIgnore(rules)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, p := range paths {
			arg := p.key
			if p.dir {
				arg += "/"
			}
			gitIgnored := exec.Command(gitPath, "-C", dir, "check-ignore", "-q", "--no-index", arg).Run() == nil
			if got := ig.Match(p.key, p.dir); got != gitIgnored {
				t.Errorf("rules %q: Match(%q, dir %v) = %v, git says %v", rules, p.key, p.dir, got, gitIgnored)
			}
		}
	}
}
