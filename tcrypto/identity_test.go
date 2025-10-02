package tcrypto_test

import (
	"bytes"
	"testing"

	"fortio.org/tsync/tcrypto"
)

func TestIdentity(t *testing.T) {
	alice, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatalf("Failed to create alice keys: %v", err)
	}
	bob, err := tcrypto.NewIdentity()
	if err != nil {
		t.Fatalf("Failed to create bob keys: %v", err)
	}
	// exchange public keys (in real life over the network)
	alicePubStr := alice.PublicKeyToString()
	t.Logf("Alice public key : %s", alicePubStr)
	// re-create public keys from strings
	alicePub2, err := tcrypto.IdentityPublicKeyString(alicePubStr)
	if err != nil {
		t.Fatalf("Failed to decode alice public key: %v", err)
	}
	AssertBytesEqual(t, "Alice public key", alice.PublicKey, alicePub2)
	// Mess it up on purpose to test failure
	badStr := "AA" + alicePubStr[2:]
	aliceBadPub, err := tcrypto.IdentityPublicKeyString(badStr)
	t.Logf("Got alice from   : %s -> %x with error: %v", badStr, aliceBadPub, err)
	if err != nil {
		t.Errorf("Got unexpected error decoding alice changed public key of right size: %v", err)
	}
	if bytes.Equal(aliceBadPub, alice.PublicKey) {
		t.Fatalf("Alice bad public key is equal to original")
	}
	// Same with bob's key
	bobPubStr := bob.PublicKeyToString()
	t.Logf("Bob public key   : %s", bobPubStr)
	bobPub2, err := tcrypto.IdentityPublicKeyString(bobPubStr)
	if err != nil {
		t.Fatalf("Failed to decode bob public key: %v", err)
	}
	AssertBytesEqual(t, "Bob public key", bob.PublicKey, bobPub2)
	// Sign something with alice's private key and verify with her public key
	msg := []byte("This is another test message")
	signedMsg := alice.SignMessage(msg)
	t.Logf("Signed message  : %s", signedMsg)
	verifiedMsg, err := tcrypto.VerifySignedMessage(signedMsg, alicePub2)
	if err != nil {
		t.Fatalf("Failed to verify signed message: %v", err)
	}
	AssertBytesEqual(t, "Verified message should be same as original", msg, verifiedMsg)
	// Try to verify with bob's public key (should fail)
	_, err = tcrypto.VerifySignedMessage(signedMsg, bobPub2)
	if err == nil {
		t.Fatalf("Unexpectedly verified alice's signed message with bob's public key")
	}
	t.Logf("Got expected error verifying with wrong public key: %v", err)
	// Try to verify a tampered message (should fail)
	tamperedSignedMsg := signedMsg[:len(signedMsg)-1] + "A"
	_, err = tcrypto.VerifySignedMessage(tamperedSignedMsg, alicePub2)
	if err == nil {
		t.Fatalf("Unexpectedly verified a tampered signed message")
	}
	t.Logf("Got expected error verifying tampered message: %v", err)
}
