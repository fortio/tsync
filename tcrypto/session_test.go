package tcrypto_test

import (
	"errors"
	"testing"

	"fortio.org/tsync/tcrypto"
)

func newTestSessions(t *testing.T) (*tcrypto.Session, *tcrypto.Session) {
	t.Helper()
	aliceID, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	aliceEph, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		t.Fatal(err)
	}
	bobEph, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		t.Fatal(err)
	}
	alice, err := tcrypto.NewSession(aliceEph, bobEph.PublicKey, aliceID.PublicKey, bobID.PublicKey)
	if err != nil {
		t.Fatalf("alice session: %v", err)
	}
	bob, err := tcrypto.NewSession(bobEph, aliceEph.PublicKey, bobID.PublicKey, aliceID.PublicKey)
	if err != nil {
		t.Fatalf("bob session: %v", err)
	}
	return alice, bob
}

func expectDecryptErr(t *testing.T, msg string, err error) {
	t.Helper()
	if _, ok := errors.AsType[*tcrypto.DecryptError](err); !ok {
		t.Fatalf("%s: expected DecryptError, got %v", msg, err)
	}
	t.Logf("%s: got expected error %v", msg, err)
}

func TestSessionRoundTrip(t *testing.T) {
	alice, bob := newTestSessions(t)
	for i, m := range []string{"hello bob", "", "multi\nline\nclip"} {
		ct := alice.Seal([]byte(m))
		pt, err := bob.Open(ct)
		if err != nil {
			t.Fatalf("bob open %d: %v", i, err)
		}
		AssertBytesEqual(t, "alice->bob", []byte(m), pt)
		// and the other direction
		ct = bob.Seal([]byte(m))
		pt, err = alice.Open(ct)
		if err != nil {
			t.Fatalf("alice open %d: %v", i, err)
		}
		AssertBytesEqual(t, "bob->alice", []byte(m), pt)
	}
}

func TestSessionFailures(t *testing.T) {
	alice, bob := newTestSessions(t)
	ct := alice.Seal([]byte("first"))
	// Wrong direction: alice can't open what she sealed herself.
	_, err := alice.Open(ct)
	expectDecryptErr(t, "wrong direction", err)
	// Tampered.
	bad := append([]byte{}, ct...)
	bad[0] ^= 1
	_, err = bob.Open(bad)
	expectDecryptErr(t, "tampered", err)
	// Failed opens don't advance the counter so the genuine frame still works.
	if _, err = bob.Open(ct); err != nil {
		t.Fatalf("genuine frame after failures: %v", err)
	}
	// Replay.
	_, err = bob.Open(ct)
	expectDecryptErr(t, "replay", err)
	// Reorder/drop: skip one frame.
	_ = alice.Seal([]byte("second"))
	third := alice.Seal([]byte("third"))
	_, err = bob.Open(third)
	expectDecryptErr(t, "reorder", err)
	// Unrelated session can't open.
	_, eve := newTestSessions(t)
	_, err = eve.Open(alice.Seal([]byte("not for eve")))
	expectDecryptErr(t, "other session", err)
}

func TestSessionSameEphemeral(t *testing.T) {
	id, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	eph, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tcrypto.NewSession(eph, eph.PublicKey, id.PublicKey, id.PublicKey)
	if err == nil {
		t.Fatalf("expected error for reflected ephemeral key")
	}
	t.Logf("got expected error %v", err)
}

func TestTrustedStorage(t *testing.T) {
	s := &tcrypto.Storage{Dir: t.TempDir()}
	trusted, err := s.LoadTrusted()
	if err != nil || len(trusted) != 0 {
		t.Fatalf("expected empty trusted map and no error on missing file, got %v %v", trusted, err)
	}
	id1, _ := tcrypto.NewIdentity()
	id2, _ := tcrypto.NewIdentity()
	k1, k2 := id1.PublicKeyToString(), id2.PublicKeyToString()
	weirdName := "my \"laptop\" with spaces\nand newline"
	if err = s.AddTrusted(k1, "host1"); err != nil {
		t.Fatal(err)
	}
	if err = s.AddTrusted(k2, weirdName); err != nil {
		t.Fatal(err)
	}
	if err = s.AddTrusted(k1, "host1-renamed"); err != nil {
		t.Fatal(err)
	}
	trusted, err = s.LoadTrusted()
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 2 || trusted[k1] != "host1-renamed" || trusted[k2] != weirdName {
		t.Errorf("unexpected trusted map: %q", trusted)
	}
}
