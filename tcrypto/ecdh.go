// Package tcrypto has all of tsync's crypto: the long term Ed25519 [Identity] of a machine (signing, and its
// short [HumanHash] that users compare), its [Storage] on disk with the trusted peers' keys, the X25519
// [Ephemeral] keys of a handshake and the AES-GCM [Session] derived from them.
package tcrypto

import (
	"crypto/ecdh"
	"crypto/rand"
)

// Ephemeral holds a private/public keypair for X25519 ECDH.
type Ephemeral struct {
	PrivateKey *ecdh.PrivateKey
	PublicKey  *ecdh.PublicKey
}

// NewEphemeralKeys generates a new X25519 key pair, to use for one handshake.
func NewEphemeralKeys() (*Ephemeral, error) {
	// Smallest keys (and secure enough too).
	var err error
	c := &Ephemeral{}
	c.PrivateKey, err = ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	c.PublicKey = c.PrivateKey.PublicKey()
	return c, nil
}

// PublicKeyToString returns the public key in text form (see [StringToPublicKey]).
func (c *Ephemeral) PublicKeyToString() string {
	return EncodeBytes(PublicKeyPrefix, c.PublicKey.Bytes())
}

// StringToPublicKey decodes an ephemeral (X25519) public key from [Ephemeral.PublicKeyToString].
func StringToPublicKey(s string) (*ecdh.PublicKey, error) {
	bytes, err := DecodeBytes(PublicKeyPrefix, s)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(bytes)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// SharedSecret derives the ECDH shared secret from our private key and the peer's public key. Not a key by
// itself: see [Identity.VerifiedSession] for the authenticated session keys.
func (c *Ephemeral) SharedSecret(peerPublicKey *ecdh.PublicKey) ([]byte, error) {
	return c.PrivateKey.ECDH(peerPublicKey)
}
