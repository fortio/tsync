package main

import (
	"strconv"

	"fortio.org/terminal/ansipixels"
)

// MinLogLines is the minimum number of lines kept for the log area.
const MinLogLines = 3

// LogArea keeps the (scrolling) log output in the bottom part of the screen, below a
// fixed top area, using an ANSI scroll region (DECSTBM). The logger output (see
// [ansipixels.AnsiPixels.FlushLogger]) resumes at the saved cursor position, which we
// keep at the bottom of the region so it scrolls only that part of the screen.
// Candidate to move to ansipixels once it settles.
type LogArea struct {
	ap  *ansipixels.AnsiPixels
	top int // number of lines reserved at the top.
	h   int // screen height the region was set for.
}

func NewLogArea(ap *ansipixels.AnsiPixels) *LogArea {
	return &LogArea{ap: ap, top: -1}
}

// SetTop reserves the first top lines of the screen, clamped to leave at least
// MinLogLines for the log, and returns the number of top lines actually reserved.
// The caller is responsible for (re)drawing the top area, and for restoring the
// cursor (to the log position) after drawing.
func (la *LogArea) SetTop(top int) int {
	ap := la.ap
	top = max(0, min(top, ap.H-MinLogLines))
	if top == la.top && ap.H == la.h {
		return top
	}
	// Rows given back to the log area still have the old top content.
	for y := max(top, 0); y < la.top && y < ap.H; y++ {
		ap.MoveCursor(0, y)
		ap.ClearEndOfLine()
	}
	// DECSTBM rows are 1 based and inclusive; it also moves the cursor home.
	ap.WriteString("\033[" + strconv.Itoa(top+1) + ";" + strconv.Itoa(ap.H) + "r")
	ap.MoveCursor(0, ap.H-1)
	ap.SaveCursorPos()
	la.top, la.h = top, ap.H
	return top
}

// Reset restores the full screen scroll region, with the cursor on the last line.
func (la *LogArea) Reset() {
	la.ap.WriteString("\033[r")
	la.ap.MoveCursor(0, la.ap.H-1)
	la.top = -1
}
