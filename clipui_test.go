package main

import (
	"strings"
	"testing"
	"time"

	"fortio.org/smap"
	"fortio.org/tsync/tsnet"
)

func TestClipPanel(t *testing.T) {
	ap, buf, bw := newTestAP(40, 24)
	cp := NewClipPanel(ap, 3)
	now := time.Now()
	peers := []smap.KV[tsnet.Peer, tsnet.PeerData]{
		{Key: tsnet.Peer{Name: "nothing"}, Value: tsnet.PeerData{}},
		{Key: tsnet.Peer{Name: "older"}, Value: tsnet.PeerData{Clip: "old\x1b[31m clip", ClipTime: now.Add(-time.Minute)}},
		{Key: tsnet.Peer{Name: "newer"}, Value: tsnet.PeerData{Clip: "a\nb\nc\nd\ne", ClipTime: now}},
	}
	if _, ok := cp.Latest(); ok {
		t.Errorf("Latest before any draw should be empty")
	}
	cp.SetOurs("sent text", 2)
	boxes := cp.Layout(peers, 100)
	if len(boxes) != 3 {
		t.Fatalf("expected 3 boxes (ours + 2 peers with clips), got %d", len(boxes))
	}
	if boxes[0].from != "us" || !strings.Contains(boxes[0].title, "sent to 2 peers") {
		t.Errorf("unexpected first box %+v", boxes[0])
	}
	if boxes[1].from != "older" || boxes[1].entry.text != "old clip" || !strings.HasPrefix(boxes[1].title, " 2 older ") {
		t.Errorf("unexpected 2nd box (sanitized, peer index 2) %+v", boxes[1])
	}
	if strings.Join(boxes[2].lines, "|") != "a|b|… (+3 lines)" {
		t.Errorf("unexpected 3rd box lines %q", boxes[2].lines)
	}
	h := cp.Draw(10, boxes)
	_ = bw.Flush()
	if h != 3+3+5 || buf.Len() == 0 {
		t.Errorf("unexpected drawn height %d (output %d bytes)", h, buf.Len())
	}
	// Mouse coordinates are 1 based: line 10 (0 based) is the top of our box.
	if b, ok := cp.Clicked(11); !ok || b.from != "us" {
		t.Errorf("click on first box line got %+v %v", b, ok)
	}
	if b, ok := cp.Clicked(10 + 3 + 3 + 5); !ok || b.from != "newer" {
		t.Errorf("click on last box line got %+v %v", b, ok)
	}
	if _, ok := cp.Clicked(10 + 3 + 3 + 5 + 1); ok {
		t.Errorf("click below boxes should not match")
	}
	if b, ok := cp.Latest(); !ok || b.from != "newer" {
		t.Errorf("Latest got %+v %v", b, ok)
	}
	buf.Reset()
	cp.Copy(boxes[2])
	_ = bw.Flush()
	if !strings.HasPrefix(buf.String(), "\x1b]52;c;") {
		t.Errorf("expected OSC 52 output, got %q", buf.String())
	}
	// Short screen: fewer lines then fewer boxes.
	boxes = cp.Layout(peers, 8) // ours (3) + older (3), not enough left for newer.
	if len(boxes) != 2 || boxes[1].from != "older" {
		t.Errorf("unexpected layout for small height: %d boxes", len(boxes))
	}
	cp.ours = clipEntry{}
	boxes = cp.Layout(peers[2:], 4) // only room for 2 content lines.
	if len(boxes) != 1 || strings.Join(boxes[0].lines, "|") != "a|… (+4 lines)" {
		t.Errorf("unexpected reduced box: %+v", boxes)
	}
	// Peer gone: dropped from the cache.
	cp.Layout(peers[:2], 100)
	if _, ok := cp.cache[peers[1].Key]; !ok || len(cp.cache) != 1 {
		t.Errorf("expected cache to only keep current peers, has %d", len(cp.cache))
	}
}
