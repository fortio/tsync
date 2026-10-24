package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/tsnet"
)

// SyncPanel shows the state of the sync folders (a dry run for now), one line per folder and linked peer
// having it, in a box below the files one.
type SyncPanel struct {
	ap  *ansipixels.AnsiPixels
	srv *tsnet.Server
}

type syncLine struct {
	text  string
	color tcolor.BasicColor
}

func NewSyncPanel(ap *ansipixels.AnsiPixels, srv *tsnet.Server) *SyncPanel {
	return &SyncPanel{ap: ap, srv: srv}
}

// SyncLine is the line shown for a sync folder with a peer.
func SyncLine(st tsnet.SyncStatus) syncLine {
	what := "📁 " + st.Folder
	switch {
	case st.Err != "":
		return syncLine{what + ": ❌ " + st.Err, tcolor.BrightRed}
	case !st.Scanned:
		return syncLine{what + ": scanning " + ShortPath(st.Path) + "…", tcolor.Cyan}
	case st.Peer == (tsnet.Peer{}):
		return syncLine{fmt.Sprintf("%s (%d items): no linked peer has it", what, st.Files), tcolor.Cyan}
	}
	peer := SanitizeName(st.Peer.Name)
	what += " ↔ " + peer
	switch {
	case !st.Ready:
		return syncLine{what + ": waiting for its list", tcolor.Cyan}
	case st.Summary.Pending() == 0:
		return syncLine{what + " · " + st.Summary.Describe(peer), tcolor.BrightGreen}
	case len(st.Summary.Conflicts) > 0:
		return syncLine{what + " · " + st.Summary.Describe(peer), tcolor.BrightYellow}
	default:
		return syncLine{what + " · " + st.Summary.Describe(peer), tcolor.BrightCyan}
	}
}

// Lines returns the lines to show (none without sync folders).
func (sp *SyncPanel) Lines() []syncLine {
	sts := sp.srv.SyncStatus()
	lines := make([]syncLine, 0, len(sts))
	for _, st := range sts {
		lines = append(lines, SyncLine(st))
	}
	return lines
}

// Draw draws the box with the lines at y and returns its height (0 if no lines).
func (sp *SyncPanel) Draw(y int, lines []syncLine) int {
	if len(lines) == 0 {
		return 0
	}
	ap := sp.ap
	h := len(lines) + 2
	ap.DrawRoundBox(0, y, ap.W, h)
	title, _ := TruncateRightToFit(ap, " 🔄 sync · dry run, nothing is written yet ", ap.W-4)
	ap.WriteAt(2, y, "%s", DarkGray(title))
	for i, l := range lines {
		text, _ := TruncateRightToFit(ap, SanitizeName(l.text), ap.W-4)
		ap.WriteAt(2, y+1+i, "%s", Color16(l.color, text))
	}
	return h
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
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	abs, err := filepath.Abs(path)
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
