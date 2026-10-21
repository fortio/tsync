package main

import (
	"fmt"
	"sync"
	"time"

	"fortio.org/log"
	"fortio.org/terminal/ansipixels/tcolor"
)

// noticeFor is how long a notice replaces the help line.
const noticeFor = 4 * time.Second

// Notice is a transient message shown in the separator line (replacing the help),
// so feedback is visible even when the log output is redirected.
type Notice struct {
	mu    sync.Mutex
	msg   string
	color tcolor.BasicColor
	at    time.Time
	dirty bool // needs a repaint (shown or expired).
}

// Notices is the single notice shared by the UI and the actions.
var Notices Notice

func (n *Notice) set(color tcolor.BasicColor, msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msg, n.color, n.at, n.dirty = msg, color, time.Now(), true
}

// Infof logs and shows a (green) notice.
func (n *Notice) Infof(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Infof("%s", msg)
	n.set(tcolor.BrightGreen, msg)
}

// Warnf logs and shows a (yellow) notice.
func (n *Notice) Warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Warnf("%s", msg)
	n.set(tcolor.BrightYellow, msg)
}

// Errf logs and shows a (red) notice.
func (n *Notice) Errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Errf("%s", msg)
	n.set(tcolor.BrightRed, msg)
}

// Current returns the notice to show, if any and not expired.
func (n *Notice) Current() (string, tcolor.BasicColor, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.at.IsZero() || time.Since(n.at) >= noticeFor {
		return "", 0, false
	}
	return " " + n.msg + " ", n.color, true
}

// NeedsRepaint returns true (once) when the notice must be shown or removed.
func (n *Notice) NeedsRepaint() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.dirty {
		n.dirty = false
		return true
	}
	if !n.at.IsZero() && time.Since(n.at) >= noticeFor {
		n.at = time.Time{}
		return true
	}
	return false
}
