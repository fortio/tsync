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
