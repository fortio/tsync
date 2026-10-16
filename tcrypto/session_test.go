package tcrypto_test

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fortio.org/tsync/tcrypto"
)

const testContext = "connect1 alice>bob"

func newTestSessions(t *testing.T) (*tcrypto.Session, *tcrypto.Session) {
	t.Helper()
	aliceID, bobID, aliceEph, bobEph := newTestKeys(t)
	alice, err := aliceID.VerifiedSession(aliceEph, bobID.SignEphemeral(bobEph, testContext), testContext, bobID.PublicKey)
	if err != nil {
		t.Fatalf("alice session: %v", err)
	}
	bob, err := bobID.VerifiedSession(bobEph, aliceID.SignEphemeral(aliceEph, testContext), testContext, aliceID.PublicKey)
	if err != nil {
		t.Fatalf("bob session: %v", err)
	}
	return alice, bob
}

func newTestKeys(t *testing.T) (*tcrypto.Identity, *tcrypto.Identity, *tcrypto.Ephemeral, *tcrypto.Ephemeral) {
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
	return aliceID, bobID, aliceEph, bobEph
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

func TestSessionContextSeparation(t *testing.T) {
	// Even if (incorrectly) reusing the same ephemeral keys, different contexts must get different keys.
	aliceID, bobID, aliceEph, bobEph := newTestKeys(t)
	const otherContext = "other context"
	alice1, err := aliceID.VerifiedSession(aliceEph, bobID.SignEphemeral(bobEph, testContext), testContext, bobID.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	bob2, err := bobID.VerifiedSession(bobEph, aliceID.SignEphemeral(aliceEph, otherContext), otherContext, aliceID.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bob2.Open(alice1.Seal([]byte("context 1 frame")))
	expectDecryptErr(t, "cross context", err)
}

func TestVerifiedSessionFailures(t *testing.T) {
	aliceID, bobID, aliceEph, bobEph := newTestKeys(t)
	bobSigned := bobID.SignEphemeral(bobEph, testContext)
	for _, tc := range []struct {
		name    string
		signed  string
		context string
		theirID ed25519.PublicKey
	}{
		{"wrong identity (mitm)", aliceID.SignEphemeral(bobEph, testContext), testContext, bobID.PublicKey},
		{"wrong context", bobSigned, "accept1 bob>alice", bobID.PublicKey},
		{"tampered", bobSigned[:len(bobSigned)-3] + "AAA", testContext, bobID.PublicKey},
		{"bad identity size", bobSigned, testContext, bobID.PublicKey[:5]},
		{"not an ephemeral key", bobID.SignMessage([]byte("hello")), testContext, bobID.PublicKey},
		// reflection: our own signed ephemeral sent back to us.
		{"reflected", aliceID.SignEphemeral(aliceEph, testContext), testContext, aliceID.PublicKey},
	} {
		_, err := aliceID.VerifiedSession(aliceEph, tc.signed, tc.context, tc.theirID)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		t.Logf("%s: got expected error %v", tc.name, err)
	}
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
	for _, bad := range []string{"", "p.", k1[:len(k1)-2], k1 + "\np.injected \"x\"", "x" + k1[1:]} {
		if err = s.AddTrusted(bad, "bad"); err == nil {
			t.Errorf("expected error adding invalid key %q", bad)
		}
	}
	trusted, err = s.LoadTrusted()
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 2 || trusted[k1] != "host1-renamed" || trusted[k2] != weirdName {
		t.Errorf("unexpected trusted map: %q", trusted)
	}
	// Manually edited file without trailing newline.
	id3, _ := tcrypto.NewIdentity()
	k3 := id3.PublicKeyToString()
	fname := filepath.Join(s.Dir, tcrypto.ValidatedPublicKeysFile)
	b, err := os.ReadFile(fname)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fname, append(b, []byte("# no newline comment")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.AddTrusted(k3, "host3"); err != nil {
		t.Fatal(err)
	}
	trusted, err = s.LoadTrusted()
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 3 || trusted[k3] != "host3" {
		t.Errorf("unexpected trusted map after no trailing newline append: %q", trusted)
	}
	// Corrupted file (eg bad manual edit) is rejected.
	f, err := os.OpenFile(fname, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("p.short \"bad\"\n")
	_ = f.Close()
	_, err = s.LoadTrusted()
	if err == nil {
		t.Fatalf("expected error loading corrupted trusted file")
	}
	t.Logf("got expected error %v", err)
}
