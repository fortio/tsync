package tcrypto

type SignatureInvalidError struct {
	Msg string
}

func (e *SignatureInvalidError) Error() string {
	return "signature invalid: " + e.Msg
}

func NewSignatureInvalidErr(msg string) error {
	return &SignatureInvalidError{Msg: msg}
}

type EncodingError struct {
	Msg string
}

func (e *EncodingError) Error() string {
	return "encoding error: " + e.Msg
}

func NewEncodingErr(msg string) error {
	return &EncodingError{Msg: msg}
}
