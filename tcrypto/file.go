package tcrypto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Default directory (in the home directory) and files of a [Storage].
const (
	TsyncDir                = ".tsync"
	PrivateIdentityFile     = "id"          // our private key, only readable by the user.
	PublicIdentityFile      = "id.pub"      // our public key.
	ValidatedPublicKeysFile = "checked.pub" // the peers accepted by the user: one `<public key> "name"` per line.
)

// Storage is the directory where our identity and the trusted peers' keys are kept.
type Storage struct {
	Dir string // Full path to the .tsync directory
}

// InitStorage returns the default storage, ~/.tsync, creating the directory if needed.
func InitStorage() (*Storage, error) {
	hdir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return InitStorageDir(filepath.Join(hdir, TsyncDir))
}

// InitStorageDir is [InitStorage] with another directory than ~/.tsync (created, with its parents, if needed).
func InitStorageDir(dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { // public readable as only PrivateIdentityFile is sensitive.
		return nil, err
	}
	return &Storage{Dir: dir}, nil
}

// SaveIdentity writes the identity's private and public key files.
func (s *Storage) SaveIdentity(id *Identity) error {
	filePath := filepath.Join(s.Dir, PrivateIdentityFile)
	b := []byte(id.PrivateKeyToString() + "\n")
	err := os.WriteFile(filePath, b, 0o600) // private key only readable by user
	if err != nil {
		return err
	}
	b = []byte(id.PublicKeyToString() + "\n")
	filePath = filepath.Join(s.Dir, PublicIdentityFile)
	return os.WriteFile(filePath, b, 0o644) //nolint:gosec // public key readable by all
}

// LoadIdentity reads our identity. It is an error if there is none yet, or if the public key file doesn't
// match the private key.
func (s *Storage) LoadIdentity() (*Identity, error) {
	filePath := filepath.Join(s.Dir, PrivateIdentityFile)
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
	pubFilePath := filepath.Join(s.Dir, PublicIdentityFile)
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

// LoadTrusted returns the trusted (validated through TOFU) peer public keys
// mapped to the name the peer had when it was accepted.
// A missing file is not an error (no trusted peers yet).
func (s *Storage) LoadTrusted() (map[string]string, error) {
	res := map[string]string{}
	b, err := os.ReadFile(filepath.Join(s.Dir, ValidatedPublicKeysFile))
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		pubKey, quotedName, _ := strings.Cut(line, " ")
		if err = ValidatePublicKeyString(pubKey); err != nil {
			return nil, NewEncodingErr(fmt.Sprintf("%s line %d: invalid public key %q: %v", ValidatedPublicKeysFile, i+1, pubKey, err))
		}
		name, err := strconv.Unquote(quotedName)
		if err != nil {
			return nil, NewEncodingErr(fmt.Sprintf("%s line %d: invalid name %q: %v", ValidatedPublicKeysFile, i+1, quotedName, err))
		}
		res[pubKey] = name
	}
	return res, nil
}

// AddTrusted appends a peer public key (and its current name) to the trusted keys file.
// Callers should check [Storage.LoadTrusted] first to avoid duplicates (last one wins on load).
func (s *Storage) AddTrusted(pubKey, name string) error {
	if err := ValidatePublicKeyString(pubKey); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, ValidatedPublicKeysFile),
		os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	// Don't glue our record to the last line if the file (eg manually edited) lacks a trailing newline.
	prefix := ""
	if fi, serr := f.Stat(); serr == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, rerr := f.ReadAt(last, fi.Size()-1); rerr == nil && last[0] != '\n' {
			prefix = "\n"
		}
	}
	_, err = fmt.Fprintf(f, "%s%s %q\n", prefix, pubKey, name)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
