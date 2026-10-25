//go:build !windows && !(darwin && cgo)

package tsnet

import (
	"runtime"
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// SyncWatchDefault is the default of -sync-watch: true when watching is cheap (inotify on Linux; kqueue elsewhere
// needs a file descriptor per file).
const SyncWatchDefault = runtime.GOOS == "linux"

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = syscall.ENOSPC // inotify watches limit.
	errTooManyFiles error = syscall.EMFILE
)

// watcher watches each directory (see syncwatch_windows.go for Windows, syncwatch_darwin.go for macOS with cgo).
type watcher = fsnotify.Watcher

func newWatcher() (*watcher, error) {
	return fsnotify.NewWatcher()
}
