package tcrypto

import "encoding/base64"

// EncodeBytes returns the prefix (eg [PublicKeyPrefix]) followed by b in unpadded URL safe base64.
func EncodeBytes(prefix string, b []byte) string {
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// DecodeBytes is the reverse of [EncodeBytes]: checks the prefix and decodes what follows.
func DecodeBytes(prefix, s string) ([]byte, error) {
	l := len(prefix)
	if len(s) < l || s[:l] != prefix {
		return nil, NewEncodingErr("invalid prefix")
	}
	return base64.RawURLEncoding.DecodeString(s[l:])
}
