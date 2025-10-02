package tcrypto

import (
	"crypto/ed25519"
	"strings"
)

type Identity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

const (
	SignedPrefix = "s:"
)

func (id *Identity) SignMessage(message []byte) string {
	signature := ed25519.Sign(id.PrivateKey, message)
	return SignedPrefix + EncodeBytes(message) + ":" + EncodeBytes(signature)
}

func VerifySignedMessage(signedMessage string, pubKey ed25519.PublicKey) ([]byte, error) {
	if len(signedMessage) < len(SignedPrefix) || signedMessage[:len(SignedPrefix)] != SignedPrefix {
		return nil, NewSignatureInvalidErr("invalid signed message prefix")
	}
	parts := strings.SplitN(signedMessage[len(SignedPrefix):], ":", 3)
	if len(parts) != 2 {
		return nil, NewSignatureInvalidErr("invalid signed message format")
	}
	message, err := DecodeBytes(parts[0])
	if err != nil {
		return nil, NewSignatureInvalidErr("failed to decode message")
	}
	signature, err := DecodeBytes(parts[1])
	if err != nil {
		return nil, NewSignatureInvalidErr("failed to decode signature")
	}
	if !ed25519.Verify(pubKey, message, signature) {
		return nil, NewSignatureInvalidErr("signature verification failed")
	}
	return message, nil
}

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

func IdentityFromPrivateKey(privKeyStr string) (*Identity, error) {
	privKeyBytes, err := DecodeBytes(privKeyStr)
	if err != nil {
		return nil, err
	}
	privKey := ed25519.PrivateKey(privKeyBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)
	return &Identity{
		PrivateKey: privKey,
		PublicKey:  pubKey,
	}, nil
}

func (id *Identity) PrivateKeyToString() string {
	return EncodeBytes(id.PrivateKey)
}

func (id *Identity) PublicKeyToString() string {
	return EncodeBytes(id.PublicKey)
}

func IdentityPublicKeyString(s string) (ed25519.PublicKey, error) {
	bytes, err := DecodeBytes(s)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(bytes), nil
}
