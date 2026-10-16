package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fortio.org/log"
	"fortio.org/smap"
	"fortio.org/terminal/ansipixels"
	"fortio.org/tsync/tsnet"
)

// ClipPanel draws the clipboard boxes (ours, then each peer that sent us one) below
// the peers table and remembers where they are for mouse clicks.
type ClipPanel struct {
	ap       *ansipixels.AnsiPixels
	MaxLines int // max content lines per box.
	ours     clipEntry
	cache    map[tsnet.Peer]clipEntry // sanitized peer clips (keyed on ClipTime).
	boxes    []clipBox                // as last drawn.
}

type clipEntry struct {
	text string
	when time.Time
	sent int // ours only: how many peers it was sent to.
}

// clipBox is one box to draw (from Layout) / that was drawn (for clicks).
type clipBox struct {
	title string
	from  string
	entry clipEntry
	lines []string
	y     int // top line once drawn.
}

func (b *clipBox) height() int {
	return len(b.lines) + 2
}

func NewClipPanel(ap *ansipixels.AnsiPixels, maxLines int) *ClipPanel {
	return &ClipPanel{ap: ap, MaxLines: maxLines, cache: make(map[tsnet.Peer]clipEntry)}
}

// SetOurs records the clip we just sent.
func (cp *ClipPanel) SetOurs(text string, sent int) {
	cp.ours = clipEntry{text: text, when: time.Now(), sent: sent}
}

func (cp *ClipPanel) peerEntry(peer tsnet.Peer, data tsnet.PeerData) clipEntry {
	e, ok := cp.cache[peer]
	if !ok || !e.when.Equal(data.ClipTime) {
		e = clipEntry{text: SanitizeClip(data.Clip), when: data.ClipTime}
		cp.cache[peer] = e
	}
	return e
}

func clipTitle(prefix string, e clipEntry, extra string) string {
	return fmt.Sprintf(" %s %s · %s%s ", prefix, e.when.Format("15:04:05"), sizeStr(len(e.text)), extra)
}

func sizeStr(n int) string {
	if n == 1 {
		return "1 byte"
	}
	return strconv.Itoa(n) + " bytes"
}

// Layout returns the boxes to draw, fitting in maxHeight lines (boxes get fewer
// content lines, then are omitted, when there isn't enough room).
func (cp *ClipPanel) Layout(peers []smap.KV[tsnet.Peer, tsnet.PeerData], maxHeight int) []clipBox {
	var boxes []clipBox
	if !cp.ours.when.IsZero() {
		extra := " · sent to " + strconv.Itoa(cp.ours.sent) + " peer"
		if cp.ours.sent != 1 {
			extra += "s"
		}
		boxes = append(boxes, clipBox{title: clipTitle("🏠 sent", cp.ours, extra), from: "us", entry: cp.ours})
	}
	oldCache := cp.cache
	cp.cache = make(map[tsnet.Peer]clipEntry) // only keep current peers.
	for i, kv := range peers {
		if kv.Value.ClipTime.IsZero() {
			continue
		}
		if e, ok := oldCache[kv.Key]; ok {
			cp.cache[kv.Key] = e
		}
		e := cp.peerEntry(kv.Key, kv.Value)
		name := SanitizeName(kv.Key.Name)
		boxes = append(boxes, clipBox{title: clipTitle(strconv.Itoa(i+1)+" "+name, e, ""), from: name, entry: e})
	}
	width := cp.ap.W - 4 // borders and 1 space padding on each side.
	res := boxes[:0]
	for _, b := range boxes {
		lines := min(cp.MaxLines, maxHeight-2)
		if lines < 1 {
			break
		}
		b.lines = ClipLines(cp.ap, b.entry.text, width, lines)
		maxHeight -= b.height()
		res = append(res, b)
	}
	return res
}

// Draw draws the boxes starting at line y and returns the total height.
func (cp *ClipPanel) Draw(y int, boxes []clipBox) int {
	ap := cp.ap
	start := y
	for i := range boxes {
		b := &boxes[i]
		b.y = y
		ap.DrawRoundBox(0, y, ap.W, b.height())
		title, _ := TruncateRightToFit(ap, b.title, ap.W-4)
		ap.WriteAt(2, y, "%s", DarkGray(title))
		for j, l := range b.lines {
			ap.WriteAt(2, y+1+j, "%s", l)
		}
		y += b.height()
	}
	cp.boxes = boxes
	return y - start
}

// Clicked returns the box at screen line my (1 based mouse coordinates).
func (cp *ClipPanel) Clicked(my int) (clipBox, bool) {
	y := my - 1
	for _, b := range cp.boxes {
		if y >= b.y && y < b.y+b.height() {
			return b, true
		}
	}
	return clipBox{}, false
}

// Latest returns the most recently received peer clip box (as last drawn).
func (cp *ClipPanel) Latest() (clipBox, bool) {
	var best clipBox
	found := false
	for _, b := range cp.boxes {
		if b.from != "us" && (!found || b.entry.when.After(best.entry.when)) {
			best, found = b, true
		}
	}
	return best, found
}

// Copy copies the box content to our clipboard (OSC 52).
func (cp *ClipPanel) Copy(b clipBox) {
	cp.ap.CopyToClipboard(b.entry.text)
	log.Infof("Copied %s from %s to the clipboard", sizeStr(len(b.entry.text)), b.from)
}

// SanitizeName makes a (peer provided) name safe to display on one line.
func SanitizeName(name string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(SanitizeClip(name))
}
