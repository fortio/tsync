package main

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"fortio.org/terminal/ansipixels"
)

// Clipboard text handling: bracketed paste collection, sanitizing and display lines.

const (
	PasteStart = "\x1b[200~"
	PasteEnd   = "\x1b[201~"
)

// PasteCollector separates bracketed paste content from the rest of the terminal input,
// across reads (a paste can span many reads, and markers can be split between reads).
type PasteCollector struct {
	MaxSize  int // paste content beyond this is dropped (Truncated is set).
	inPaste  bool
	buf      []byte
	pending  []byte // possible start of a marker at the end of the previous data.
	idle     int    // consecutive empty Feed calls while pending.
	tooLarge bool
}

// pendingIdleRelease is how many consecutive empty Feed calls (ticks) before a held back
// partial marker is released as regular input (eg a lone ESC key).
const pendingIdleRelease = 3

// Paste is a completed bracketed paste.
type Paste struct {
	Text      string
	Truncated bool // more than MaxSize was pasted.
}

// InPaste returns true while in the middle of a paste.
func (pc *PasteCollector) InPaste() bool {
	return pc.inPaste
}

// Feed processes the next input data and returns the input that is not part of a
// paste, and the pastes completed in this data. Call with empty data when there
// is no new input (every tick) so a held back partial marker (eg a lone ESC) gets
// released after a few idle ticks.
func (pc *PasteCollector) Feed(data []byte) (other []byte, pastes []Paste) {
	if len(pc.pending) > 0 {
		if len(data) == 0 {
			pc.idle++
			if pc.idle < pendingIdleRelease || pc.inPaste {
				return nil, nil
			}
			other, pc.pending, pc.idle = pc.pending, nil, 0
			return other, nil
		}
		data = append(pc.pending, data...)
		pc.pending, pc.idle = nil, 0
	}
	for len(data) > 0 {
		if !pc.inPaste {
			idx := bytes.Index(data, []byte(PasteStart))
			if idx < 0 {
				keep := partialSuffix(data, PasteStart)
				other = append(other, data[:len(data)-keep]...)
				pc.pending = append(pc.pending, data[len(data)-keep:]...)
				return other, pastes
			}
			other = append(other, data[:idx]...)
			data = data[idx+len(PasteStart):]
			pc.inPaste = true
			continue
		}
		idx := bytes.Index(data, []byte(PasteEnd))
		if idx < 0 {
			keep := partialSuffix(data, PasteEnd)
			pc.add(data[:len(data)-keep])
			pc.pending = append(pc.pending, data[len(data)-keep:]...)
			return other, pastes
		}
		pc.add(data[:idx])
		pastes = append(pastes, Paste{Text: string(pc.buf), Truncated: pc.tooLarge})
		pc.buf, pc.tooLarge, pc.inPaste = nil, false, false
		data = data[idx+len(PasteEnd):]
	}
	return other, pastes
}

func (pc *PasteCollector) add(b []byte) {
	if pc.MaxSize > 0 && len(pc.buf)+len(b) > pc.MaxSize {
		b = b[:max(0, pc.MaxSize-len(pc.buf))]
		pc.tooLarge = true
	}
	pc.buf = append(pc.buf, b...)
}

// partialSuffix returns the length of the longest suffix of data that is a proper prefix of marker.
func partialSuffix(data []byte, marker string) int {
	for n := min(len(marker)-1, len(data)); n > 0; n-- {
		if bytes.HasSuffix(data, []byte(marker[:n])) {
			return n
		}
	}
	return 0
}

// SanitizeClip makes (peer provided) clipboard text safe to display in and paste back
// into a terminal: removes escape sequences (CSI, OSC, ...) and control characters
// other than newline and tab, normalizes line endings, replaces invalid UTF-8 and
// removes bidi override/isolate characters.
func SanitizeClip(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == '\x1b' {
			i += escapeLen(s[i:])
			continue
		}
		i += size
		switch {
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r == '\r':
			sb.WriteRune('\n')
		case unicode.IsControl(r): // C0, DEL and C1.
		case isBidiControl(r):
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// escapeLen returns the length of the escape sequence at the start of s (which starts with ESC).
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters/intermediates then a final byte in 0x40-0x7e.
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: until BEL or ST (ESC \).
		for i := 2; i < len(s); i++ {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == '\x1b' {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // unterminated, let the next ESC be handled on its own.
			}
		}
		return len(s)
	default: // 2 byte sequence (eg ESC 7).
		return 2
	}
}

func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C
}

// TruncateRightToFit returns the start of s that fits in maxWidth screen columns, with an
// ellipsis if truncated, and its width. s must not contain escape sequences.
// Candidate for ansipixels (counterpart of TruncateLeftToFit).
func TruncateRightToFit(ap *ansipixels.AnsiPixels, s string, maxWidth int) (string, int) {
	if maxWidth <= 0 {
		return "", 0
	}
	w := ap.ScreenWidth(s)
	if w <= maxWidth {
		return s, w
	}
	runes := []rune(s)
	// No rune is less than 0 width so at most maxWidth runes can fit (ignoring 0 width combining ones,
	// which we'd just cut a bit early).
	runes = runes[:min(len(runes), maxWidth)]
	// Binary search the longest prefix that fits with the ellipsis.
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ap.ScreenWidth(string(runes[:mid])) <= maxWidth-1 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	res := string(runes[:lo]) + "…"
	return res, ap.ScreenWidth(res)
}

// ClipLines returns at most maxLines display lines (each fitting in width) for the
// (sanitized) clip text, the last one summarizing how many more lines there are if needed.
func ClipLines(ap *ansipixels.AnsiPixels, text string, width, maxLines int) []string {
	maxLines = max(1, maxLines)
	text = strings.TrimSuffix(text, "\n") // copied text often ends with a newline, don't show an empty line.
	parts := strings.SplitN(text, "\n", maxLines+1)
	more := 0
	if len(parts) > maxLines {
		more = len(parts) - maxLines + strings.Count(parts[maxLines], "\n")
		parts = parts[:maxLines]
	}
	if more > 0 {
		// replace last displayed line by the summary.
		more++
		parts[maxLines-1] = "… (+" + strconv.Itoa(more) + " lines)"
	}
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ReplaceAll(p, "\t", "    ")
		l, _ := TruncateRightToFit(ap, p, width)
		lines = append(lines, l)
	}
	return lines
}
