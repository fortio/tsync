package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitDropTokens(t *testing.T) {
	for _, tc := range []struct {
		in  string
		out []string
		ok  bool
	}{
		{`/a/b.txt`, []string{"/a/b.txt"}, true},
		{`/a/My\ File.txt`, []string{"/a/My File.txt"}, true},
		{`'/a/My File.txt' `, []string{"/a/My File.txt"}, true},
		{`'/a/it'\''s.txt'`, []string{"/a/it's.txt"}, true},
		{`"C:\World of Warcraft\_classic_beta_\WTF\Config.wtf"`, []string{`C:\World of Warcraft\_classic_beta_\WTF\Config.wtf`}, true},
		{`C:\dir\file.txt`, []string{`C:\dir\file.txt`}, true},
		{`\\server\share\f.txt`, []string{`\\server\share\f.txt`}, true},
		{`/a/x /b/y`, []string{"/a/x", "/b/y"}, true},
		{`'/a/x' '/b/y' `, []string{"/a/x", "/b/y"}, true},
		{`"/a/x" "/b/y"`, []string{"/a/x", "/b/y"}, true},
		{`'unbalanced`, nil, false},
		{`trailing\`, []string{`trailing\`}, true},
	} {
		got, ok := splitDropTokens(tc.in)
		if ok != tc.ok || strings.Join(got, "|") != strings.Join(tc.out, "|") {
			t.Errorf("splitDropTokens(%q) = %q %v, expected %q %v", tc.in, got, ok, tc.out, tc.ok)
		}
	}
}

func TestDroppedFiles(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plain := mk("plain.txt")
	spaced := mk("My File.txt")
	quote := mk("it's.txt")
	pct := mk("100% done.txt")
	fileURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(spaced)}).String()
	for _, tc := range []struct {
		name string
		in   string
		out  []string
	}{
		{"macos plain", plain, []string{plain}},
		{"macos escaped", strings.ReplaceAll(spaced, " ", `\ `), []string{spaced}},
		{"linux single quotes + trailing space", "'" + spaced + "' ", []string{spaced}},
		{"linux quote in name", "'" + strings.ReplaceAll(quote, "'", `'\''`) + "'", []string{quote}},
		{"windows double quotes", `"` + spaced + `"`, []string{spaced}},
		{"file uri", fileURI, []string{spaced}},
		{"file uri with percent", (&url.URL{Scheme: "file", Path: filepath.ToSlash(pct)}).String(), []string{pct}},
		{"localhost file uri", strings.Replace(fileURI, "file://", "file://localhost", 1), []string{spaced}},
		{"multiple", "'" + plain + "' '" + spaced + "' ", []string{plain, spaced}},
		{"directory", dir, []string{dir}},
		{"surrounding newline", plain + "\n", []string{plain}},
	} {
		got, ok := DroppedFiles(tc.in)
		if !ok || strings.Join(got, "|") != strings.Join(tc.out, "|") {
			t.Errorf("%s: DroppedFiles(%q) = %q %v, expected %q", tc.name, tc.in, got, ok, tc.out)
		}
	}
	for _, text := range []string{
		"",
		"hello world",
		filepath.Join(dir, "missing.txt"),
		plain + " " + filepath.Join(dir, "missing.txt"), // all must exist.
		"plain.txt", // relative: text (even if it existed in the current directory).
		plain + "\n" + spaced,
		"'" + plain,
		"file://otherhost" + filepath.ToSlash(plain),
		"see " + plain,
		strings.Repeat(plain+" ", maxDropFiles+1),
	} {
		if got, ok := DroppedFiles(text); ok {
			t.Errorf("DroppedFiles(%q) should be text, got %q", text, got)
		}
	}
}
