package tcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
)

const (
	sessionInfo   = "tsync1 session"
	sessionKeyLen = 32 // AES-256
)

// Session holds the 2 directional AEAD (AES-256-GCM) states derived from an
// ephemeral ECDH exchange. Nonces are implicit 64-bit counters so frames must be
// opened in the same order they were sealed (ie over an ordered stream like TCP);
// any replay, reorder or drop makes Open fail.
type Session struct {
	sendMu  sync.Mutex
	send    cipher.AEAD
	sendCtr uint64
	recvMu  sync.Mutex
	recv    cipher.AEAD
	recvCtr uint64
}

// SignEphemeral returns our ephemeral public key, bound to the given handshake
// context, signed with our long term identity. It is meant to be sent to the peer
// which passes it to [Identity.VerifiedSession] with the same context.
// Both sides must use the same context (eg protocol version and both names in
// a canonical order) as it is also mixed into the session keys derivation.
func (id *Identity) SignEphemeral(eph *Ephemeral, context string) string {
	return id.SignMessage(ephemeralTranscript(context, eph.PublicKey.Bytes()))
}

func ephemeralTranscript(context string, ephPub []byte) []byte {
	return append([]byte(sessionInfo+" eph "+context+"\x00"), ephPub...)
}

// VerifiedSession checks that peerSigned is the peer's ephemeral public key signed
// (by [Identity.SignEphemeral], with the same context) by theirID and only then
// derives the session from our ephemeral keys and theirs. theirID must be the peer
// identity the caller expects (ie discovered and human/TOFU verified), as this is
// what authenticates the session.
func (id *Identity) VerifiedSession(eph *Ephemeral, peerSigned, context string, theirID ed25519.PublicKey) (*Session, error) {
	msg, err := VerifySignedMessage(peerSigned, theirID)
	if err != nil {
		return nil, err
	}
	prefix := ephemeralTranscript(context, nil)
	if !bytes.HasPrefix(msg, prefix) {
		return nil, NewSignatureInvalidErr("signed ephemeral key context mismatch")
	}
	peerEph, err := ecdh.X25519().NewPublicKey(msg[len(prefix):])
	if err != nil {
		return nil, NewEncodingErr("invalid peer ephemeral key: " + err.Error())
	}
	return newSession(eph, peerEph, id.PublicKey, theirID, context)
}

// newSession derives a session from our ephemeral keys and the peer's ephemeral
// public key. Both long term identities are bound into the derivation so the same
// session keys can only be obtained by these 2 identities, as is the context so
// different handshake contexts get different keys. Both sides get matching
// send/recv keys without needing to know who initiated. Unexported as it doesn't
// check peerEph belongs to theirID, use [Identity.VerifiedSession].
func newSession(eph *Ephemeral, peerEph *ecdh.PublicKey, ourID, theirID ed25519.PublicKey, context string) (*Session, error) {
	secret, err := eph.SharedSecret(peerEph)
	if err != nil {
		return nil, err
	}
	ourEph := eph.PublicKey.Bytes()
	theirEph := peerEph.Bytes()
	cmp := bytes.Compare(ourEph, theirEph)
	if cmp == 0 {
		return nil, errors.New("peer ephemeral key is identical to ours")
	}
	lowEph, highEph := ourEph, theirEph
	lowID, highID := ourID, theirID
	if cmp > 0 {
		lowEph, highEph = highEph, lowEph
		lowID, highID = highID, lowID
	}
	salt := append(append([]byte{}, lowEph...), highEph...)
	// identities are fixed size so appending the context after is unambiguous.
	info := sessionInfo + string(lowID) + string(highID) + context
	keys, err := hkdf.Key(sha256.New, secret, salt, info, 2*sessionKeyLen)
	if err != nil {
		return nil, err
	}
	// first key is for low -> high direction, second for high -> low.
	lowToHigh, err := newGCM(keys[:sessionKeyLen])
	if err != nil {
		return nil, err
	}
	highToLow, err := newGCM(keys[sessionKeyLen:])
	if err != nil {
		return nil, err
	}
	s := &Session{send: lowToHigh, recv: highToLow}
	if cmp > 0 {
		s.send, s.recv = s.recv, s.send
	}
	return s, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func counterNonce(aead cipher.AEAD, ctr uint64) []byte {
	nonce := make([]byte, aead.NonceSize())
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], ctr)
	return nonce
}

// Seal encrypts and authenticates the plaintext for the next outgoing frame.
func (s *Session) Seal(plaintext []byte) []byte {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	nonce := counterNonce(s.send, s.sendCtr)
	s.sendCtr++
	return s.send.Seal(nil, nonce, plaintext, nil)
}

// Open decrypts and authenticates the next incoming frame.
func (s *Session) Open(ciphertext []byte) ([]byte, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	nonce := counterNonce(s.recv, s.recvCtr)
	plaintext, err := s.recv.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, NewDecryptErr(err.Error())
	}
	s.recvCtr++
	return plaintext, nil
}
