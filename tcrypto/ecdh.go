// tcrypto library for tsync crypto operations (key generation etc).
package tcrypto

import (
	"crypto/ecdh"
	"crypto/rand"
)

// Ephemeral holds a private/public keypair for X25519 ECDH.
type Ephemeral struct {
	Curve      ecdh.Curve
	PrivateKey *ecdh.PrivateKey
	PublicKey  *ecdh.PublicKey
}

func NewEphemeralKeys() (*Ephemeral, error) {
	// Smallest keys (and secure enough too).
	curve := ecdh.X25519()
	var err error
	c := &Ephemeral{Curve: curve}
	c.PrivateKey, err = curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	c.PublicKey = c.PrivateKey.PublicKey()
	return c, nil
}

func (c *Ephemeral) PublicKeyToString() string {
	return EncodeBytes(PublicKeyPrefix, c.PublicKey.Bytes())
}

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

func (c *Ephemeral) SharedSecret(peerPublicKey *ecdh.PublicKey) ([]byte, error) {
	// Derives the shared secret using private key and peer's public key
	return c.PrivateKey.ECDH(peerPublicKey)
}
