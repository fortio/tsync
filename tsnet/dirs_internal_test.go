package tsnet

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeRelPath(t *testing.T) {
	for _, bad := range []string{
		"", "..", "../x", "a/../b", "a/..", "/abs", "a//b", "a/", "./a", "a/./b",
		strings.Repeat("d/", maxDirDepth) + "f",
	} {
		if parts, err := safeRelPath(bad); err == nil {
			t.Errorf("safeRelPath(%q) = %q, expected an error", bad, parts)
		}
	}
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"a/b c/con.txt", []string{"a", "b c", "_con.txt"}},
		{`a\b`, []string{"a_b"}},
		{`..\..\x`, []string{".._.._x"}},
		{".git/hooks/.x", []string{".git", "hooks", ".x"}},
		{"a\x00b/c\nd", []string{"a_b", "c_d"}},
		{"C:/x", []string{"C_", "x"}},
		{"x:/c", []string{"x_", "c"}},
		{"... /x", []string{"file", "x"}},
	} {
		parts, err := safeRelPath(tc.in)
		if err != nil || !slices.Equal(parts, tc.want) {
			t.Errorf("safeRelPath(%q) = %q %v, expected %q", tc.in, parts, err, tc.want)
		}
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

// The receiver giving up on a directory fails it on our side too, and stops its files in progress.
func TestDirSendRefused(t *testing.T) {
	s := (&Config{Name: "test"}).NewServer()
	l := &link{outgoing: make(map[uint32]*Transfer), incoming: make(map[uint32]*recvFile)}
	p := &Transfer{ID: 10, Name: "d", Dir: true, Files: 2, State: TransferActive}
	k1 := &Transfer{ID: 11, Source: "a", State: TransferActive, parent: p}
	k2 := &Transfer{ID: 12, Source: "b/c", State: TransferActive, parent: p, sent: true}
	p.addKid(k1)
	p.addKid(k2)
	s.transfers = []*Transfer{p}
	l.outgoing[10], l.outgoing[11], l.outgoing[12] = p, k1, k2
	s.outgoingResult(Peer{Name: "peer"}, l, 10, errors.New("disk full"))
	if p.State != TransferFailed || !strings.Contains(p.Err, "disk full") || l.outgoing[10] != nil {
		t.Errorf("directory not failed/forgotten: %+v", p)
	}
	if k1.cancel == "" || k2.cancel == "" {
		t.Errorf("files in progress not canceled: %q %q", k1.cancel, k2.cancel)
	}
	// A file confirmed after that doesn't revive the directory.
	s.outgoingResult(Peer{Name: "peer"}, l, 12, nil)
	if p.State != TransferFailed || l.outgoing[12] != nil {
		t.Errorf("unexpected state %v %v", p.State, l.outgoing[12])
	}
}
