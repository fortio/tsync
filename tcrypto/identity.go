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
	SignedPrefix     = "s."
	PrivateKeyPrefix = "k."
	PublicKeyPrefix  = "p."
	// MessagePrefix    = "m:".
)

func (id *Identity) SignMessage(message []byte) string {
	signature := ed25519.Sign(id.PrivateKey, message)
	return EncodeBytes(SignedPrefix, message) + "/" + EncodeBytes("", signature)
}

func VerifySignedMessage(signedMessage string, pubKey ed25519.PublicKey) ([]byte, error) {
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
	privKeyBytes, err := DecodeBytes(PrivateKeyPrefix, privKeyStr)
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
	return EncodeBytes(PrivateKeyPrefix, id.PrivateKey)
}

func (id *Identity) PublicKeyToString() string {
	return EncodeBytes(PublicKeyPrefix, id.PublicKey)
}

func (id *Identity) HumanID() string {
	return HumanHash(id.PublicKey)
}

func IdentityPublicKeyString(s string) (ed25519.PublicKey, error) {
	bytes, err := DecodeBytes(PublicKeyPrefix, s)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(bytes), nil
}
