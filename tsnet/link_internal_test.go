package tsnet

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fortio.org/tsync/tcrypto"
)

func TestFrames(t *testing.T) {
	idA, _ := tcrypto.NewIdentity()
	idB, _ := tcrypto.NewIdentity()
	ephA, _ := tcrypto.NewEphemeralKeys()
	ephB, _ := tcrypto.NewEphemeralKeys()
	const ctx = "test"
	sessA, err := idA.VerifiedSession(ephA, idB.SignEphemeral(ephB, ctx), ctx, idB.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := idB.VerifiedSession(ephB, idA.SignEphemeral(ephA, ctx), ctx, idA.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err = writeFrame(&buf, sessA, FrameHello, nil); err != nil {
		t.Fatal(err)
	}
	if err = writeFrame(&buf, sessA, FrameClip, []byte("clip")); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"H", string(FrameClip) + "clip"} {
		frame, rerr := readFrame(&buf)
		if rerr != nil {
			t.Fatal(rerr)
		}
		plain, oerr := sessB.Open(frame)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if string(plain) != expected {
			t.Errorf("got %q, expected %q", plain, expected)
		}
	}
	// Oversized frame header is rejected before allocating.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], maxFrameSize+1)
	if _, err = readFrame(bytes.NewReader(hdr[:])); err == nil {
		t.Errorf("expected error for oversized frame")
	}
	// Truncated frame.
	binary.BigEndian.PutUint32(hdr[:], 10)
	if _, err = readFrame(bytes.NewReader(append(hdr[:], 1, 2, 3))); err == nil {
		t.Errorf("expected error for truncated frame")
	}
}

func TestValidateNameAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"host1", true},
		{"My Laptop (é ✂️)", true},
		{"", false},
		{"esc\x1b[31m", false},
		{"nl\nx", false},
		{"c1\u009b", false},
		{"bad\xffutf8", false},
		{string(make([]byte, MaxNameLen+1)), false},
	} {
		err := ValidateName(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateName(%q) = %v, expected ok %t", tc.name, err, tc.ok)
		}
	}
	s := &Server{}
	d, err := s.MCastMessageDecode([]byte(`tsync1 "ok" p.key e 3 r abc`))
	if err != nil || d != (Discovery{Name: "ok", PublicKey: "p.key", Epoch: 3, RunID: "abc"}) {
		t.Errorf("valid discovery message: %+v %v", d, err)
	}
	if d, err = s.MCastMessageDecode([]byte(`tsync1 "old" p.key e 3`)); err != nil || d.RunID != "" || d.Epoch != 3 {
		t.Errorf("older version discovery message: %+v %v", d, err)
	}
	if _, err = s.MCastMessageDecode([]byte(`tsync1 "x" p.key`)); err == nil {
		t.Errorf("truncated discovery message accepted")
	}
	if _, err := s.MCastMessageDecode([]byte(`tsync1 "evil\x1b]52;c;aGk=\a" p.key e 3 r x`)); err == nil {
		t.Errorf("discovery message with escape in name accepted")
	}
}

func TestSafeFileName(t *testing.T) {
	for _, tc := range []struct{ in, out string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\x\evil.exe`, "evil.exe"},
		{`C:\World of Warcraft\_classic_beta_\WTF\Config.wtf`, "Config.wtf"},
		{"..", "file"},
		{"", "file"},
		{".bashrc", "_.bashrc"},
		{"a<b>c:d|e?f*g\"h.txt", "a_b_c_d_e_f_g_h.txt"},
		{"ctrl\x1b[31m\nname.txt", "ctrl_[31m_name.txt"},
		{"CON", "_CON"},
		{"com1.txt", "_com1.txt"},
		{"COM10.txt", "COM10.txt"},
		{"trailing dots... ", "trailing dots"},
		{"日本語 ✂️.md", "日本語 ✂️.md"},
		{"bad\xffutf8", "bad_utf8"},
	} {
		if got := SafeFileName(tc.in); got != tc.out {
			t.Errorf("SafeFileName(%q) = %q, expected %q", tc.in, got, tc.out)
		}
	}
	long := SafeFileName(strings.Repeat("é", 300) + ".txt")
	if len(long) > MaxFileNameLen || !strings.HasSuffix(long, ".txt") || !utf8.ValidString(long) {
		t.Errorf("long name not truncated properly: %d %q", len(long), long)
	}
}

func TestReserveName(t *testing.T) {
	dir := t.TempDir()
	for _, expected := range []string{"a.txt", "a-1.txt", "a-2.txt"} {
		p, err := reserveName(dir, "a.txt")
		if err != nil || filepath.Base(p) != expected {
			t.Fatalf("reserveName got %q %v, expected %q", p, err, expected)
		}
	}
	if p, err := reserveName(dir, "noext"); err != nil || filepath.Base(p) != "noext" {
		t.Errorf("unexpected %q %v", p, err)
	}
	if p, err := reserveName(dir, "noext"); err != nil || filepath.Base(p) != "noext-1" {
		t.Errorf("unexpected %q %v", p, err)
	}
}

// Normal dial fails (as if the peer was behind NAT): the peer must be asked to dial us instead.
func TestDialMeFallback(t *testing.T) {
	if os.Getenv("CI") != "" && runtime.GOOS == "darwin" {
		t.Skip("Skipping multicast test on macOS in CI (no multicast support)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	servers := make([]*Server, 0, 2)
	for _, name := range []string{"NatA", "NatB"} {
		id, _ := tcrypto.NewIdentity()
		cfg := Config{
			Name: name, Port: DefaultDiscoveryPort + 13, Mcast: "239.255.115.123", Target: DefaultTarget,
			Identity: id, BaseBroadcastInterval: 100 * time.Millisecond,
		}
		srv := cfg.NewServer()
		srv.testDialAddr = func(addr string, fallback bool) string {
			if fallback {
				return addr
			}
			return "127.0.0.1:1" // nothing listens there.
		}
		if err := srv.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer srv.Stop()
		servers = append(servers, srv)
	}
	find := func(srv *Server, name string) Peer {
		for {
			for p := range srv.Peers.All() {
				if p.Name == name {
					return p
				}
			}
			if ctx.Err() != nil {
				t.Fatalf("%s didn't discover %s", srv.Name, name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	peerB := find(servers[0], "NatB")
	peerA := find(servers[1], "NatA")
	if err := servers[0].ConnectToPeer(peerB); err != nil {
		t.Fatal(err)
	}
	for {
		if d, _ := servers[1].Peers.Get(peerA); d.Status == ReceivedConn {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("B didn't get the connection request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := servers[1].ConnectToPeer(peerA); err != nil {
		t.Fatal(err)
	}
	for !servers[0].Linked(peerB) || !servers[1].Linked(peerA) {
		if ctx.Err() != nil {
			t.Fatal("link not established through the dial me fallback")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
