package tcrypto

import (
	"os"
	"path"
	"strings"
)

const (
	TsyncDir                = ".tsync"
	PrivateIdentityFile     = "id"
	PublicIdentityFile      = "id.pub"
	ValidatedPublicKeysFile = "checked.pub"
)

func createDirectory(dir string) error {
	// Already exists ?
	_, err := os.Stat(dir)
	if err == nil {
		// Exists, nothing to do
		return nil
	}
	return os.Mkdir(dir, 0o755) // public readable as only PrivateIdentityFile is sensitive
}

type Storage struct {
	Dir string // Full path to the .tsync directory
}

func InitStorage() (s *Storage, err error) {
	// Creates the ~/.tsync directory and files if they don't exist yet.
	hdir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	s = &Storage{}
	s.Dir = path.Join(hdir, TsyncDir)
	err = createDirectory(s.Dir)
	if err != nil {
		return s, err
	}
	return s, nil
}

func (s *Storage) SaveIdentity(id *Identity) error {
	filePath := path.Join(s.Dir, PrivateIdentityFile)
	b := []byte(id.PrivateKeyToString() + "\n")
	err := os.WriteFile(filePath, b, 0o600) // private key only readable by user
	if err != nil {
		return err
	}
	b = []byte(id.PublicKeyToString() + "\n")
	filePath = path.Join(s.Dir, PublicIdentityFile)
	return os.WriteFile(filePath, b, 0o644) //nolint:gosec // public key readable by all
}

func (s *Storage) LoadIdentity() (*Identity, error) {
	filePath := path.Join(s.Dir, PrivateIdentityFile)
	privKeyBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	// trim possible newline
	privKeyStr := strings.TrimSpace(string(privKeyBytes))
	id, err := IdentityFromPrivateKey(privKeyStr)
	if err != nil {
		return nil, err
	}
	// check public key file too
	pubFilePath := path.Join(s.Dir, PublicIdentityFile)
	pubKeyBytes, err := os.ReadFile(pubFilePath)
	if err != nil {
		return nil, err
	}
	pubKeyStr := strings.TrimSpace(string(pubKeyBytes))
	if pubKeyStr != id.PublicKeyToString() {
		return nil, NewEncodingErr("public key file does not match private key")
	}
	return id, nil
}
