//go:build !windows

package tsnet

import "syscall"

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = syscall.ENOSPC // inotify watches limit.
	errTooManyFiles error = syscall.EMFILE
)
