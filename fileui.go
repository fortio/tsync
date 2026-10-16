package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fortio.org/log"
	"fortio.org/progressbar"
	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/tsnet"
)

// FilePanel shows the recent file offers and transfers, one per line in a box below the
// clipboard ones, and maps clicks on offers back to them (to download).
type FilePanel struct {
	ap       *ansipixels.AnsiPixels
	srv      *tsnet.Server
	Dir      string // where received files are saved (for display).
	MaxLines int
	local    []fileLine // our offers and local errors.
	rows     []fileLine // as last drawn (y set).
	bars     map[transferKey]*progressbar.Bar
}

type fileLine struct {
	when  time.Time
	lead  string // optional colored fixed width part (spinner, progress bar), not truncated.
	leadW int    // its screen width.
	text  string // plain text (truncated to fit).
	color tcolor.BasicColor
	offer *tsnet.Transfer // incoming offer (click/d to download).
	y     int
}

func NewFilePanel(ap *ansipixels.AnsiPixels, srv *tsnet.Server, dir string, maxLines int) *FilePanel {
	return &FilePanel{ap: ap, srv: srv, Dir: dir, MaxLines: maxLines, bars: make(map[transferKey]*progressbar.Bar)}
}

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
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

const barWidth = 20

// transferKey identifies a transfer (for its progress bar).
type transferKey struct {
	peer     tsnet.Peer
	id       uint32
	incoming bool
}

// bar returns the progress bar of an active transfer (kept between frames for its spinner).
func (fp *FilePanel) bar(t tsnet.Transfer, color string) *progressbar.Bar {
	k := transferKey{t.Peer, t.ID, t.Incoming}
	b := fp.bars[k]
	if b == nil {
		cfg := progressbar.DefaultConfig()
		cfg.Width = barWidth
		cfg.Color = color
		cfg.ScreenWriter = io.Discard // only used through Render().
		b = cfg.NewBar()
		fp.bars[k] = b
	}
	return b
}

func (fp *FilePanel) activeLine(icon string, t tsnet.Transfer, color, dir string) fileLine {
	percent := 100.
	if t.Size > 0 {
		percent = 100 * float64(t.Done) / float64(t.Size)
	}
	lead := icon + " " + fp.bar(t, color).Render(percent)
	return fileLine{
		when:  t.Start,
		lead:  lead,
		leadW: fp.ap.ScreenWidth(lead),
		text: fmt.Sprintf(" %s · %s %s %s", progressbar.RateETA(t.Done, t.Size, time.Since(t.Start)),
			t.Name, dir, SanitizeName(t.Peer.Name)),
		color: tcolor.BrightBlue,
	}
}

// doneStr is the size, and how long it took and the average speed.
func doneStr(t tsnet.Transfer) string {
	d := t.End.Sub(t.Start)
	if d <= 0 || t.Size == 0 {
		return " (" + progressbar.HumanBytes(t.Size) + ")"
	}
	return " (" + progressbar.RateETA(t.Size, t.Size, d) + ")"
}

// Offer offers the dropped file(s) to the linked peers, with feedback on errors.
func (fp *FilePanel) Offer(paths []string) {
	for _, p := range paths {
		n, err := fp.srv.OfferFile(p)
		line := fileLine{when: time.Now()}
		if err != nil {
			log.Errf("Can't offer %s: %v", p, err)
			line.text, line.color = fmt.Sprintf("❌ can't offer %s: %v", p, err), tcolor.BrightRed
		} else {
			fi, _ := os.Stat(p)
			line.text = fmt.Sprintf("📤 offered %s (%s) to %d peer%s", p, progressbar.HumanBytes(fi.Size()), n, plural(n))
			if n == 0 {
				line.text = fmt.Sprintf("📤 offered %s (%s), will be sent to peers when they connect", p, progressbar.HumanBytes(fi.Size()))
			}
			line.color = tcolor.BrightCyan
		}
		fp.local = append(fp.local, line)
	}
	if len(fp.local) > fp.MaxLines {
		fp.local = fp.local[len(fp.local)-fp.MaxLines:]
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (fp *FilePanel) transferLine(t tsnet.Transfer) fileLine {
	tc := t
	l := fileLine{when: t.Start}
	peer := SanitizeName(t.Peer.Name)
	switch {
	case t.Incoming && t.State == tsnet.TransferOffered:
		l.text = fmt.Sprintf("📥 %s offers %s (%s): click or press d to download", peer, t.Source, progressbar.HumanBytes(t.Size))
		l.color, l.offer = tcolor.BrightYellow, &tc
	case t.Incoming && t.State == tsnet.TransferActive:
		return fp.activeLine("📥", t, progressbar.GreenBar, "from")
	case t.Incoming && t.State == tsnet.TransferDone:
		l.text = fmt.Sprintf("✅ %s from %s saved as %s%s", t.Name, peer, ShortPath(t.Path), doneStr(t))
		l.color = tcolor.BrightGreen
	case t.State == tsnet.TransferActive:
		return fp.activeLine("📤", t, progressbar.BlueBar, "to")
	case t.State == tsnet.TransferDone:
		l.text = fmt.Sprintf("✅ %s sent to %s%s", t.Name, peer, doneStr(t))
		l.color = tcolor.Green
	default: // failed
		dir := "from"
		if !t.Incoming {
			dir = "to"
		}
		l.text = fmt.Sprintf("❌ %s %s %s: %s", t.Name, dir, peer, t.Err)
		l.color = tcolor.BrightRed
	}
	return l
}

// Lines returns the (at most MaxLines, most recent) lines to show.
func (fp *FilePanel) Lines() []fileLine {
	lines := slices.Clone(fp.local)
	active := make(map[transferKey]bool)
	for _, t := range fp.srv.Transfers() {
		lines = append(lines, fp.transferLine(t))
		if t.State == tsnet.TransferActive {
			active[transferKey{t.Peer, t.ID, t.Incoming}] = true
		}
	}
	for k := range fp.bars { // forget the bars of finished transfers.
		if !active[k] {
			delete(fp.bars, k)
		}
	}
	slices.SortStableFunc(lines, func(a, b fileLine) int { return a.when.Compare(b.when) })
	if len(lines) > fp.MaxLines {
		lines = lines[len(lines)-fp.MaxLines:]
	}
	return lines
}

// Draw draws the box with the lines at y and returns its height (0 if no lines).
func (fp *FilePanel) Draw(y int, lines []fileLine) int {
	fp.rows = nil
	if len(lines) == 0 {
		return 0
	}
	ap := fp.ap
	h := len(lines) + 2
	ap.DrawRoundBox(0, y, ap.W, h)
	title, _ := TruncateRightToFit(ap, " 📎 files · saved in "+ShortPath(fp.Dir)+" ", ap.W-4)
	ap.WriteAt(2, y, "%s", DarkGray(title))
	for i, l := range lines {
		l.y = y + 1 + i
		text, _ := TruncateRightToFit(ap, l.text, ap.W-4-l.leadW)
		ap.WriteAt(2, l.y, "%s%s", l.lead, Color16(l.color, text))
		fp.rows = append(fp.rows, l)
	}
	return h
}

// Clicked downloads the offer on screen line my (1 based mouse coordinates), returns true if there was one.
func (fp *FilePanel) Clicked(my int) bool {
	for _, r := range fp.rows {
		if r.y == my-1 && r.offer != nil {
			fp.download(r.offer)
			return true
		}
	}
	return false
}

// DownloadLatest downloads the most recent pending offer.
func (fp *FilePanel) DownloadLatest() {
	var latest *tsnet.Transfer
	for _, t := range fp.srv.Transfers() {
		if t.Incoming && t.State == tsnet.TransferOffered && (latest == nil || !t.Start.Before(latest.Start)) {
			latest = &t
		}
	}
	if latest == nil {
		log.Warnf("No pending file offer to download")
		return
	}
	fp.download(latest)
}

func (fp *FilePanel) download(t *tsnet.Transfer) {
	if err := fp.srv.AcceptFile(t.Peer, t.ID); err != nil {
		log.Errf("Can't download %s from %s: %v", t.Name, SanitizeName(t.Peer.Name), err)
	}
}
