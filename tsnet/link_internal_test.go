package tsnet

import (
	"bytes"
	"encoding/binary"
	"testing"

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
	for _, expected := range []string{"H", "Cclip"} {
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
