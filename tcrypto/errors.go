package tcrypto

// SignatureInvalidError is returned when a signed message can't be verified (bad format, key or signature).
type SignatureInvalidError struct {
	Msg string
}

func (e *SignatureInvalidError) Error() string {
	return "signature invalid: " + e.Msg
}

// NewSignatureInvalidErr returns a [SignatureInvalidError] with that message.
func NewSignatureInvalidErr(msg string) error {
	return &SignatureInvalidError{Msg: msg}
}

// EncodingError is returned for keys or messages that can't be decoded (wrong prefix, size, base64...).
type EncodingError struct {
	Msg string
}

func (e *EncodingError) Error() string {
	return "encoding error: " + e.Msg
}

// NewEncodingErr returns an [EncodingError] with that message.
func NewEncodingErr(msg string) error {
	return &EncodingError{Msg: msg}
}

// DecryptError is returned by [Session.Open] when a frame can't be decrypted or authenticated.
type DecryptError struct {
	Msg string
}

func (e *DecryptError) Error() string {
	return "decrypt error: " + e.Msg
}

// NewDecryptErr returns a [DecryptError] with that message.
func NewDecryptErr(msg string) error {
	return &DecryptError{Msg: msg}
}
