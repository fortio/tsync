package tcrypto

import (
	"crypto/ed25519"
	"fmt"
	"strings"
)

// Identity is the long term Ed25519 key pair of a machine: what the peers trust once the user checked its
// hash ([Identity.HumanID]).
type Identity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

// Prefixes of the text form of signed messages and keys (see [EncodeBytes]).
const (
	SignedPrefix     = "s."
	PrivateKeyPrefix = "k."
	PublicKeyPrefix  = "p."
	// MessagePrefix    = "m:".
)

// SignMessage returns the message and its signature in text form, for [VerifySignedMessage].
func (id *Identity) SignMessage(message []byte) string {
	signature := ed25519.Sign(id.PrivateKey, message)
	return EncodeBytes(SignedPrefix, message) + "/" + EncodeBytes("", signature)
}

// VerifySignedMessage checks a message from [Identity.SignMessage] was signed by pubKey and returns the message.
// The error is a [SignatureInvalidError].
func VerifySignedMessage(signedMessage string, pubKey ed25519.PublicKey) ([]byte, error) {
	if len(pubKey) != ed25519.PublicKeySize {
		// ed25519.Verify would panic.
		return nil, NewSignatureInvalidErr(fmt.Sprintf("invalid public key size %d", len(pubKey)))
	}
	parts := strings.SplitN(signedMessage, "/", 2)
	if len(parts) != 2 {
		return nil, NewSignatureInvalidErr("invalid signed message format, missing '/' separator")
	}
	message, err := DecodeBytes(SignedPrefix, parts[0])
	if err != nil {
		return nil, NewSignatureInvalidErr("failed to decode message: " + err.Error())
	}
	signature, err := DecodeBytes("", parts[1])
	if err != nil {
		return nil, NewSignatureInvalidErr("failed to decode signature: " + err.Error())
	}
	if !ed25519.Verify(pubKey, message, signature) {
		return nil, NewSignatureInvalidErr("signature verification failed")
	}
	return message, nil
}

// NewIdentity generates a new identity (not saved, see [Storage.SaveIdentity]).
func NewIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return &Identity{
		PrivateKey: priv,
		PublicKey:  pub,
	}, nil
}

// IdentityFromPrivateKey returns the identity for a private key from [Identity.PrivateKeyToString].
func IdentityFromPrivateKey(privKeyStr string) (*Identity, error) {
	privKeyBytes, err := DecodeBytes(PrivateKeyPrefix, privKeyStr)
	if err != nil {
		return nil, err
	}
	if len(privKeyBytes) != ed25519.PrivateKeySize { // Public() would panic.
		return nil, NewEncodingErr(fmt.Sprintf("invalid private key size %d", len(privKeyBytes)))
	}
	privKey := ed25519.PrivateKey(privKeyBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)
	return &Identity{
		PrivateKey: privKey,
		PublicKey:  pubKey,
	}, nil
}

// PrivateKeyToString returns the private key in text form (what is in the [PrivateIdentityFile]).
func (id *Identity) PrivateKeyToString() string {
	return EncodeBytes(PrivateKeyPrefix, id.PrivateKey)
}

// PublicKeyToString returns the public key in text form: how peers are identified in discovery messages and
// in the trusted keys file.
func (id *Identity) PublicKeyToString() string {
	return EncodeBytes(PublicKeyPrefix, id.PublicKey)
}

// HumanID returns the [HumanHash] of our public key (DDD-DDDD), what users compare before accepting a peer.
func (id *Identity) HumanID() string {
	return HumanHash(id.PublicKey)
}

// IdentityPublicKeyString decodes an identity public key from [Identity.PublicKeyToString].
func IdentityPublicKeyString(s string) (ed25519.PublicKey, error) {
	bytes, err := DecodeBytes(PublicKeyPrefix, s)
	if err != nil {
		return nil, err
	}
	if len(bytes) != ed25519.PublicKeySize {
		return nil, NewEncodingErr(fmt.Sprintf("invalid public key size %d", len(bytes)))
	}
	return ed25519.PublicKey(bytes), nil
}

// ValidatePublicKeyString checks that s is a valid and canonically encoded
// identity public key string (so it can't contain separators/whitespace).
func ValidatePublicKeyString(s string) error {
	pub, err := IdentityPublicKeyString(s)
	if err != nil {
		return err
	}
	if EncodeBytes(PublicKeyPrefix, pub) != s {
		return NewEncodingErr("non canonical public key encoding")
	}
	return nil
}
