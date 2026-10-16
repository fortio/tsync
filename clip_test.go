package main

import (
	"strings"
	"testing"
)

type feedResult struct {
	other  string
	pastes []Paste
}

func feedAll(pc *PasteCollector, chunks ...string) feedResult {
	var res feedResult
	for _, c := range chunks {
		o, p := pc.Feed([]byte(c))
		res.other += string(o)
		res.pastes = append(res.pastes, p...)
	}
	// idle ticks release any held back partial marker.
	for range pendingIdleRelease {
		o, p := pc.Feed(nil)
		res.other += string(o)
		res.pastes = append(res.pastes, p...)
	}
	return res
}

func TestPasteCollectorSplits(t *testing.T) {
	input := "ab" + PasteStart + "line1\nline2 \x1b[31mred" + PasteEnd + "q\x1b" + PasteStart + "second" + PasteEnd + "\x1b"
	expectOther := "abq\x1b\x1b"
	expectPastes := []string{"line1\nline2 \x1b[31mred", "second"}
	// Every possible split in 2 and in 3 chunks.
	for i := 0; i <= len(input); i++ {
		for j := i; j <= len(input); j += 3 {
			pc := &PasteCollector{}
			res := feedAll(pc, input[:i], input[i:j], input[j:])
			if res.other != expectOther {
				t.Fatalf("split %d,%d: other %q, expected %q", i, j, res.other, expectOther)
			}
			if len(res.pastes) != len(expectPastes) {
				t.Fatalf("split %d,%d: got %d pastes %+v", i, j, len(res.pastes), res.pastes)
			}
			for k, p := range res.pastes {
				if p.Text != expectPastes[k] || p.Truncated {
					t.Fatalf("split %d,%d: paste %d %+v, expected %q", i, j, k, p, expectPastes[k])
				}
			}
			if pc.InPaste() {
				t.Fatalf("split %d,%d: still in paste", i, j)
			}
		}
	}
}

func TestPasteCollectorInProgressAndMax(t *testing.T) {
	pc := &PasteCollector{MaxSize: 5}
	other, pastes := pc.Feed([]byte("x" + PasteStart + "0123"))
	if string(other) != "x" || len(pastes) != 0 || !pc.InPaste() {
		t.Fatalf("unexpected %q %v %v", other, pastes, pc.InPaste())
	}
	// Idle tick in the middle of a paste doesn't end it.
	if other, pastes = pc.Feed(nil); len(other) != 0 || len(pastes) != 0 || !pc.InPaste() {
		t.Fatalf("unexpected %q %v %v", other, pastes, pc.InPaste())
	}
	_, pastes = pc.Feed([]byte("456789" + PasteEnd))
	if len(pastes) != 1 || pastes[0].Text != "01234" || !pastes[0].Truncated {
		t.Fatalf("unexpected pastes %+v", pastes)
	}
	// Lone ESC is held back for a few idle ticks then released.
	if other, _ = pc.Feed([]byte("\x1b")); len(other) != 0 {
		t.Fatalf("ESC not held back: %q", other)
	}
	for i := 1; i < pendingIdleRelease; i++ {
		if other, _ = pc.Feed(nil); len(other) != 0 {
			t.Fatalf("ESC released too early (%d): %q", i, other)
		}
	}
	if other, _ = pc.Feed(nil); string(other) != "\x1b" {
		t.Fatalf("ESC not released: %q", other)
	}
	// Next paste isn't marked truncated.
	_, pastes = pc.Feed([]byte(PasteStart + "ok" + PasteEnd))
	if len(pastes) != 1 || pastes[0].Text != "ok" || pastes[0].Truncated {
		t.Fatalf("unexpected pastes %+v", pastes)
	}
}

func TestSanitizeClip(t *testing.T) {
	for _, tc := range []struct{ in, out string }{
		{"plain text", "plain text"},
		{"tab\tand\nnewline", "tab\tand\nnewline"},
		{"crlf\r\nand cr\rok", "crlf\nand cr\nok"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"osc \x1b]52;c;aGVsbG8=\x07after", "osc after"},
		{"osc st \x1b]0;title\x1b\\after", "osc st after"},
		{"paste markers " + PasteStart + "in" + PasteEnd, "paste markers in"},
		{"2 bytes \x1b7saved\x1b8", "2 bytes saved"},
		{"unterminated \x1b[12", "unterminated "},
		{"lone esc at end \x1b", "lone esc at end "},
		{"bell\a bs\b del\x7f c1\u009b nul\x00", "bell bs del c1 nul"},
		{"bidi \u202eevil\u202c \u2066x\u2069", "bidi evil x"},
		{"invalid \xff utf8", "invalid � utf8"},
		{"unicode ✂️ 日本 é", "unicode ✂️ 日本 é"},
	} {
		if got := SanitizeClip(tc.in); got != tc.out {
			t.Errorf("SanitizeClip(%q) = %q, expected %q", tc.in, got, tc.out)
		}
	}
}

func TestTruncateRightAndClipLines(t *testing.T) {
	ap, _, _ := newTestAP(80, 24)
	for _, tc := range []struct {
		in    string
		width int
		out   string
		w     int
	}{
		{"short", 10, "short", 5},
		{"exactly10!", 10, "exactly10!", 10},
		{"this is too long", 10, "this is t…", 10},
		{"日本語のテキスト", 7, "日本語…", 7},
		{"x", 0, "", 0},
	} {
		out, w := TruncateRightToFit(ap, tc.in, tc.width)
		if out != tc.out || w != tc.w {
			t.Errorf("TruncateRightToFit(%q, %d) = %q, %d; expected %q, %d", tc.in, tc.width, out, w, tc.out, tc.w)
		}
	}
	for _, tc := range []struct {
		in       string
		maxLines int
		out      []string
	}{
		{"one line", 3, []string{"one line"}},
		{"two\nlines\n", 3, []string{"two", "lines"}},
		{"1\n2\n3", 3, []string{"1", "2", "3"}},
		{"1\n2\n3\n4", 3, []string{"1", "2", "… (+2 lines)"}},
		{"1\n2\n3\n4\n5\n6", 3, []string{"1", "2", "… (+4 lines)"}},
		{"1\n2\n3", 1, []string{"… (+3 lines)"}},
		{"\ttab and a long line here", 3, []string{"    tab and…"}},
		{"", 3, []string{""}},
	} {
		got := ClipLines(ap, tc.in, 12, tc.maxLines)
		if strings.Join(got, "|") != strings.Join(tc.out, "|") {
			t.Errorf("ClipLines(%q, %d) = %q, expected %q", tc.in, tc.maxLines, got, tc.out)
		}
	}
}
