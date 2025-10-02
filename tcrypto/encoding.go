package tcrypto

import "encoding/base64"

func EncodeBytes(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeBytes(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
