package main

import (
	"bufio"
	"bytes"
	"testing"

	"fortio.org/terminal/ansipixels"
)

func newTestAP(w, h int) (*ansipixels.AnsiPixels, *bytes.Buffer, *bufio.Writer) {
	ap := ansipixels.NewAnsiPixels(0)
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	ap.Out = bw
	ap.W, ap.H = w, h
	return ap, &buf, bw
}

func TestLogArea(t *testing.T) {
	ap, buf, bw := newTestAP(80, 24)
	la := NewLogArea(ap)
	check := func(top, expectTop int, expectOut string) {
		t.Helper()
		buf.Reset()
		got := la.SetTop(top)
		_ = bw.Flush()
		if got != expectTop {
			t.Errorf("SetTop(%d) = %d, expected %d", top, got, expectTop)
		}
		if buf.String() != expectOut {
			t.Errorf("SetTop(%d) output %q, expected %q", top, buf.String(), expectOut)
		}
	}
	check(5, 5, "\033[6;24r\033[24;1H\0337")
	check(5, 5, "") // no change, no output.
	// Shrinking gives lines back to the log area: they get cleared.
	check(3, 3, "\033[4;1H\033[K\033[5;1H\033[K\033[4;24r\033[24;1H\0337")
	// Clamped to keep MinLogLines.
	check(100, 24-MinLogLines, "\033[22;24r\033[24;1H\0337")
	// Resize re-applies even with same top.
	ap.H = 30
	check(100, 30-MinLogLines, "\033[28;30r\033[30;1H\0337")
	buf.Reset()
	la.Reset()
	_ = bw.Flush()
	if buf.String() != "\033[r\033[30;1H" {
		t.Errorf("Reset output %q", buf.String())
	}
}
