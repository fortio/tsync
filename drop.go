package main

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Drag and drop of files into the terminal: most terminals paste the path(s) of the
// dropped file(s), quoted or escaped in various ways, eg:
//   - macOS (iTerm2, Ghostty...): /Users/me/My\ File.txt
//   - Linux (GNOME/VTE): '/home/me/My File.txt' (with a trailing space)
//   - Windows Terminal: "C:\Users\me\My File.txt"
//   - some file managers/terminals: file:///home/me/My%20File.txt
// So a paste is considered a file drop when it's a short single line made only of
// absolute paths (or file URIs) that exist.

const (
	maxDropLen   = 4096
	maxDropFiles = 20
)

// DroppedFiles returns the paths if text looks like file(s) dropped into the terminal.
func DroppedFiles(text string) ([]string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxDropLen || strings.ContainsAny(text, "\r\n") {
		return nil, false
	}
	tokens, ok := splitDropTokens(text)
	if !ok || len(tokens) == 0 || len(tokens) > maxDropFiles {
		return nil, false
	}
	paths := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		p, ok := dropTokenPath(tok)
		if !ok {
			return nil, false
		}
		if _, err := os.Stat(p); err != nil {
			return nil, false // not an existing path: regular text.
		}
		paths = append(paths, p)
	}
	return paths, true
}

// dropTokenPath returns the absolute path for a token (path or file URI).
func dropTokenPath(tok string) (string, bool) {
	if strings.HasPrefix(tok, "file://") {
		u, err := url.Parse(tok)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return "", false
		}
		p := u.Path
		// file:///C:/dir/file on Windows.
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		tok = filepath.FromSlash(p)
	}
	if !isAbsPath(tok) {
		return "", false
	}
	return filepath.Clean(tok), true
}

// isAbsPath accepts unix absolute paths and Windows ones (drive or UNC), on any OS
// (filepath.IsAbs only knows about the current one).
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") && runtime.GOOS != "windows" {
		return true
	}
	return isWindowsPath(p)
}

func isWindowsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && isLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// splitDropTokens splits like a shell would the quoted/escaped paths: '...' (literal),
// "..." (literal, no escapes as Windows paths have backslashes), and outside quotes a
// backslash escapes the next character, except in Windows paths (C:\..., \\server\...).
// Adjacent parts form one token (how a quote is put inside a single quoted string).
// Returns false on unbalanced quotes.
func splitDropTokens(s string) ([]string, bool) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	windows := false // current token is a Windows path: backslashes are literal.
	flush := func() {
		if inToken {
			tokens = append(tokens, cur.String())
		}
		cur.Reset()
		inToken, windows = false, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t':
			flush()
		case '\'', '"':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return nil, false
			}
			cur.WriteString(s[i+1 : i+1+end])
			inToken = true
			i += end + 1
		case '\\':
			if !inToken && isWindowsPath(s[i:]) {
				windows = true
			}
			inToken = true
			if windows || i+1 >= len(s) {
				cur.WriteByte(c)
				continue
			}
			i++
			cur.WriteByte(s[i])
		default:
			if !inToken && isWindowsPath(s[i:]) {
				windows = true
			}
			inToken = true
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens, true
}
