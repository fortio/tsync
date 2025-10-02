package tcrypto_test

import (
	"bytes"
	"testing"

	"fortio.org/tsync/tcrypto"
)

func AssertBytesEqual(t *testing.T, msg string, a, b []byte) {
	if !bytes.Equal(a, b) {
		t.Fatalf("%s: Bytes differ %x vs %x", msg, a, b)
	}
}

func TestECDH(t *testing.T) {
	alice, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		t.Fatalf("Failed to create alice keys: %v", err)
	}
	bob, err := tcrypto.NewEphemeralKeys()
	if err != nil {
		t.Fatalf("Failed to create bob keys: %v", err)
	}
	// exchange public keys (in real life over the network)
	alicePubStr := alice.PublicKeyToString()
	t.Logf("Alice public key : %s", alicePubStr)
	// re-create public keys from strings
	alicePub2, err := tcrypto.StringToPublicKey(alicePubStr)
	if err != nil {
		t.Fatalf("Failed to decode alice public key: %v", err)
	}
	AssertBytesEqual(t, "Alice public key", alice.PublicKey.Bytes(), alicePub2.Bytes())
	// Mess it up on purpose to test failure
	badStr := "AA" + alicePubStr[2:]
	aliceBadPub, err := tcrypto.StringToPublicKey(badStr)
	t.Logf("Got alice from   : %s -> %x with error: %v", badStr, aliceBadPub.Bytes(), err)
	if err != nil {
		t.Errorf("Got unexpected error decoding alice changed public key of right size: %v", err)
	}
	if bytes.Equal(aliceBadPub.Bytes(), alice.PublicKey.Bytes()) {
		t.Fatalf("Alice bad public key is equal to original")
	}
	// Mess up further (short key so invalid)
	badStr = "AAAA"
	aliceBadPub, err = tcrypto.StringToPublicKey(badStr)
	t.Logf("Got alice from   : %s -> %p with error: %v", badStr, aliceBadPub, err)
	if err == nil {
		t.Errorf("Didn't get expected error decoding alice changed public key of wrong size: %p", aliceBadPub)
	}
	// Same with bob's key
	bobPubStr := bob.PublicKeyToString()
	t.Logf("Bob public key   : %s", bobPubStr)
	bobPub2, err := tcrypto.StringToPublicKey(bobPubStr)
	if err != nil {
		t.Fatalf("Failed to decode bob public key: %v", err)
	}
	AssertBytesEqual(t, "Bob public key", bob.PublicKey.Bytes(), bobPub2.Bytes())
	// derive shared secrets
	aliceSecret, err := alice.SharedSecret(bobPub2)
	if err != nil {
		t.Fatalf("Failed to create alice shared secret: %v", err)
	}
	t.Logf("Alice secret     : %x", aliceSecret)
	bobSecret, err := bob.SharedSecret(alicePub2)
	if err != nil {
		t.Fatalf("Failed to create bob shared secret: %v", err)
	}
	t.Logf("Bob secret       : %x", bobSecret)
	if len(aliceSecret) != 32 {
		t.Errorf("Wrong size for alice secret: %d", len(aliceSecret))
	}
	if len(bobSecret) != 32 {
		t.Errorf("Wrong size for bob secret: %d", len(bobSecret))
	}
	AssertBytesEqual(t, "Shared secret should be same", aliceSecret, bobSecret)
}
