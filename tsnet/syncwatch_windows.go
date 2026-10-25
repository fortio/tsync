//go:build windows

package tsnet

import "golang.org/x/sys/windows"

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = windows.ERROR_NOT_ENOUGH_QUOTA
	errTooManyFiles error = windows.ERROR_TOO_MANY_OPEN_FILES
)
