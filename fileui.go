package main

import (
	"fmt"
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
	return &FilePanel{ap: ap, srv: srv, Dir: dir, MaxLines: maxLines}
}

// DefaultDownloadDir returns ~/Downloads if it exists, the current directory otherwise.
func DefaultDownloadDir() string {
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

// HumanSize formats a byte count, eg 1.5 MiB.
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return sizeStr(int(n))
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// ShortPath replaces the home directory prefix by ~.
func ShortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

const barWidth = 20

// progressBar renders a progress bar with 1/8th of a character resolution, same look as
// fortio.org/progressbar (whose rendering isn't exported, only its building blocks), followed by the
// percentage. Its screen width is width + 7.
func progressBar(done, size int64, width int, color string) string {
	frac := 1.0
	if size > 0 {
		frac = min(1, float64(done)/float64(size))
	}
	count := int(8*float64(width)*frac + 0.5)
	full, rem := count/8, count%8
	spaces := width - full
	if rem > 0 {
		spaces--
	}
	return color + strings.Repeat(progressbar.Full, full) + progressbar.FractionalBlocks[rem] +
		strings.Repeat(progressbar.Space, spaces) + progressbar.Reset + fmt.Sprintf(" %5.1f%%", 100*frac)
}

// spinner returns the current spinner character (animated by repaints, ie progress updates).
func spinner() string {
	return progressbar.SpinnerChars[time.Now().UnixMilli()/100%int64(len(progressbar.SpinnerChars))]
}

// rateStr returns the transfer rate and, if not done, the estimated time left.
func rateStr(done, size int64, elapsed time.Duration) string {
	if done <= 0 || elapsed <= 0 {
		return ""
	}
	speed := float64(done) / elapsed.Seconds()
	res := HumanSize(int64(speed)) + "/s"
	if done < size {
		left := time.Duration(float64(size-done) / speed * float64(time.Second))
		res += ", " + progressbar.HumanDuration(left) + " left"
	}
	return res
}

func activeLine(icon string, t tsnet.Transfer, color, dir string) fileLine {
	return fileLine{
		when:  t.Start,
		lead:  icon + " " + spinner() + progressBar(t.Done, t.Size, barWidth, color),
		leadW: 2 + 1 + 2 + barWidth + 7,
		text: fmt.Sprintf(" %s/%s %s · %s %s %s", HumanSize(t.Done), HumanSize(t.Size),
			rateStr(t.Done, t.Size, time.Since(t.Start)), t.Name, dir, SanitizeName(t.Peer.Name)),
		color: tcolor.BrightBlue,
	}
}

// doneStr is how long it took and the average speed.
func doneStr(t tsnet.Transfer) string {
	d := t.End.Sub(t.Start)
	if d <= 0 || t.Size == 0 {
		return ""
	}
	return fmt.Sprintf(" in %s (%s)", progressbar.HumanDuration(d), rateStr(t.Size, t.Size, d))
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
			line.text = fmt.Sprintf("📤 offered %s (%s) to %d peer%s", p, HumanSize(fi.Size()), n, plural(n))
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

func transferLine(t tsnet.Transfer) fileLine {
	tc := t
	l := fileLine{when: t.Start}
	peer := SanitizeName(t.Peer.Name)
	switch {
	case t.Incoming && t.State == tsnet.TransferOffered:
		l.text = fmt.Sprintf("📥 %s offers %s (%s): click or press d to download", peer, t.Source, HumanSize(t.Size))
		l.color, l.offer = tcolor.BrightYellow, &tc
	case t.Incoming && t.State == tsnet.TransferActive:
		return activeLine("📥", t, progressbar.GreenBar, "from")
	case t.Incoming && t.State == tsnet.TransferDone:
		l.text = fmt.Sprintf("✅ %s (%s) from %s saved as %s%s", t.Name, HumanSize(t.Size), peer, ShortPath(t.Path), doneStr(t))
		l.color = tcolor.BrightGreen
	case t.State == tsnet.TransferActive:
		return activeLine("📤", t, progressbar.BlueBar, "to")
	case t.State == tsnet.TransferDone:
		l.text = fmt.Sprintf("✅ %s (%s) sent to %s%s", t.Name, HumanSize(t.Size), peer, doneStr(t))
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
	for _, t := range fp.srv.Transfers() {
		lines = append(lines, transferLine(t))
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
