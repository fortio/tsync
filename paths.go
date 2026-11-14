package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fortio.org/tsync/tsnet"
)

// Paths from the flags and for display: download directory, ~ expansion and shortening, -sync label=path.

// DownloadDir returns dir if not empty, else ~/Downloads if it exists, else the current directory.
func DownloadDir(dir string) string {
	if dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// ShortPath replaces the home directory prefix by ~.
func ShortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home); ok && (rest == "" || os.IsPathSeparator(rest[0])) {
		return "~" + rest
	}
	return p
}

// ExpandHome replaces a leading ~/ by the home directory (for flags given as name=~/dir, which shells don't
// expand).
func ExpandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// ParseSyncFolder parses a -sync flag value: name=path (path must be a directory, a leading ~/ is the
// home directory).
func ParseSyncFolder(s string) (tsnet.SyncFolder, error) {
	name, path, ok := strings.Cut(s, "=")
	if !ok || path == "" {
		return tsnet.SyncFolder{}, fmt.Errorf("expected name=path, got %q", s)
	}
	if err := tsnet.ValidateSyncName(name); err != nil {
		return tsnet.SyncFolder{}, err
	}
	abs, err := filepath.Abs(ExpandHome(path))
	if err != nil {
		return tsnet.SyncFolder{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return tsnet.SyncFolder{}, err
	}
	if !fi.IsDir() {
		return tsnet.SyncFolder{}, fmt.Errorf("%q is not a directory", abs)
	}
	return tsnet.SyncFolder{Name: name, Path: abs}, nil
}
