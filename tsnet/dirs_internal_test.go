package tsnet

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeRelPath(t *testing.T) {
	for _, bad := range []string{
		"", "..", "../x", "a/../b", "a/..", "/abs", "a//b", "a/", "./a", "a/./b", `a\b`, "a\x00b", "a/b\nc", "C:",
		strings.Repeat("d/", maxDirDepth) + "f",
	} {
		if parts, err := safeRelPath(bad); err == nil {
			t.Errorf("safeRelPath(%q) = %q, expected an error", bad, parts)
		}
	}
	parts, err := safeRelPath("a/b c/con.txt")
	if err != nil || len(parts) != 3 || parts[0] != "a" || parts[1] != "b c" || parts[2] != "_con.txt" {
		t.Errorf("unexpected result %q %v", parts, err)
	}
}

func TestPacerSharedRate(t *testing.T) {
	var p pacer
	start := time.Now()
	var wg sync.WaitGroup
	for range 4 { // 4 senders sharing the link: 4 * 5 * 1000 bytes at 100 000 B/s is 0.2s, not 0.05s.
		wg.Go(func() {
			for range 5 {
				p.wait(1000, 100_000)
			}
		})
	}
	wg.Wait()
	if d := time.Since(start); d < 190*time.Millisecond || d > time.Second {
		t.Errorf("unexpected duration %v", d)
	}
}
