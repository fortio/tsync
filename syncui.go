package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tsnet"
)

// SyncPanel shows the state of the sync folders, one line per folder and linked peer having it, in a box
// below the files one.
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
	if st.Getting > 0 {
		percent := 100.
		if st.CurSize > 0 {
			percent = 100 * float64(st.CurDone) / float64(st.CurSize)
		}
		what += fmt.Sprintf(" · ⬇ %s %.0f%%", st.CurFile, percent)
	}
	if !st.Ready {
		return syncLine{what + ": waiting for its list", tcolor.Cyan}
	}
	text := what + " · " + st.Summary.Describe(peer)
	warn := len(st.Summary.Conflicts) > 0 || st.Summary.Counts[syncdir.DeletionHeld] > 0 ||
		st.Summary.Counts[syncdir.CantStoreThere] > 0
	if st.CantStoreHere > 0 {
		text += fmt.Sprintf(" · %d from %s can't be stored here", st.CantStoreHere, peer)
		warn = true
	}
	if n := st.ConflictCopies; n > 0 {
		text += fmt.Sprintf(" · %d conflict %s to review", n, map[bool]string{true: "copy", false: "copies"}[n == 1])
		warn = true
	}
	text += listedIn(st)
	if st.ClockSkew > tsnet.MaxClockSkew || st.ClockSkew < -tsnet.MaxClockSkew {
		text += " · ⚠ clocks differ by " + st.ClockSkew.Abs().Round(100*time.Millisecond).String()
		warn = true
	}
	switch {
	case warn:
		return syncLine{text, tcolor.BrightYellow}
	case st.Summary.Pending() == 0:
		return syncLine{text, tcolor.BrightGreen}
	default:
		return syncLine{text, tcolor.BrightCyan}
	}
}

// listedIn is where the conflicts and deletions are listed (if any).
func listedIn(st tsnet.SyncStatus) string {
	if st.ConflictsFile == "" {
		return ""
	}
	return " (list in " + ShortPath(st.ConflictsFile) + ")"
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
	title, _ := TruncateRightToFit(ap, " 🔄 sync ", ap.W-4)
	ap.WriteAt(2, y, "%s", DarkGray(title))
	for i, l := range lines {
		text, _ := TruncateRightToFit(ap, SanitizeName(l.text), ap.W-4)
		ap.WriteAt(2, y+1+i, "%s", Color16(l.color, text))
	}
	return h
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
