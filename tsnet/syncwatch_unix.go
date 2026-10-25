//go:build !windows

package tsnet

import (
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = syscall.ENOSPC // inotify watches limit.
	errTooManyFiles error = syscall.EMFILE
)

// watcher watches each directory (see syncwatch_windows.go for Windows).
type watcher = fsnotify.Watcher

func newWatcher() (*watcher, error) {
	return fsnotify.NewWatcher()
}
