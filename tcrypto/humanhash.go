package tcrypto

import (
	"crypto/sha256"
)

// HumanHash returns a short human-friendly hash (7 decimal formatted DDD-DDDD).
// It is not cryptographically secure but good enough for displaying short
// identifiers to humans to differentiate between entities.
// Each digit has no bias from the input.
func HumanHash(data []byte) string {
	hashed := sha256.Sum256(data)
	result := [8]byte{}
	j := 0
	for i := 0; i < 7; {
		v := hashed[j]
		j++
		if v >= 250 {
			// reject to avoid modulo bias, note there is an infinitesimal (1 in ~3e36) chance
			// this will reach the 32th byte without having found 7 below 250 and panic.
			continue
		}
		result[i] = '0' + (v % 10)
		i++
	}
	// DDD-DDDD
	result[7] = result[3]
	result[3] = '-'
	return string(result[:])
}
