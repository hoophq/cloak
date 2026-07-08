package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvSecretKey names the passphrase for the encrypted-file backend. Its
// presence is also the opt-in switch: set it and Cloak uses the file backend
// instead of the OS keychain (the headless / CI path). Its value is the
// passphrase and is never written anywhere.
const EnvSecretKey = "CLOAK_SECRET_KEY"

// KDF and cipher parameters. PBKDF2-HMAC-SHA256 at the OWASP-recommended
// iteration count, into a 256-bit key for AES-256-GCM.
const (
	keyLen  = 32
	saltLen = 16
)

// kdfIter is a var, not a const, only so tests can lower it; production always
// uses the OWASP-recommended count.
var kdfIter = 600_000

// errWrongKey hides whether a failure was a bad passphrase or a tampered file
// — both are "you cannot read this" and neither should leak which.
var errWrongKey = errors.New("cannot decrypt credential: wrong " + EnvSecretKey + " or corrupt store")

// File stores credentials in a single AES-256-GCM encrypted file, with the
// key derived from a user passphrase. Without the passphrase the file is
// opaque; the passphrase itself never touches disk. It is the fallback for
// hosts with no OS keychain (see [Keyring]).
type File struct {
	passphrase []byte
}

// fileFormat is the on-disk envelope. Only ciphertext is secret: the KDF salt
// is public by design (a PBKDF2 salt is not a secret), and each entry stores
// its own nonce prepended to its ciphertext.
type fileFormat struct {
	Version int               `json:"version"`
	Salt    string            `json:"salt"`    // base64, KDF salt
	Entries map[string]string `json:"entries"` // name -> base64(nonce||ciphertext)
}

// NewFile returns a file-backed store using the given passphrase. The store
// file is resolved (and created) lazily on first use.
func NewFile(passphrase string) *File {
	return &File{passphrase: []byte(passphrase)}
}

func (f *File) Backend() string { return "encrypted file" }

func (f *File) Set(name, value string) error {
	ff, path, salt, err := f.load()
	if err != nil {
		return err
	}
	enc, err := seal(f.deriveKey(salt), name, value)
	if err != nil {
		return err
	}
	ff.Entries[name] = enc
	return f.save(ff, path)
}

func (f *File) Get(name string) (string, error) {
	ff, _, salt, err := f.load()
	if err != nil {
		return "", err
	}
	enc, ok := ff.Entries[name]
	if !ok {
		return "", fmt.Errorf("%w for upstream %q (re-run `cloak add %s`)", ErrNotFound, name, name)
	}
	return open(f.deriveKey(salt), name, enc)
}

func (f *File) Delete(name string) error {
	ff, path, _, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := ff.Entries[name]; !ok {
		return nil
	}
	delete(ff.Entries, name)
	return f.save(ff, path)
}

// load reads the store, returning the parsed envelope, its path, and the KDF
// salt (a fresh one for a store that does not exist yet). It never derives the
// key — callers do that only when they actually need it, so Delete and a
// missing Get do not pay the KDF cost.
func (f *File) load() (*fileFormat, string, []byte, error) {
	if len(f.passphrase) == 0 {
		return nil, "", nil, fmt.Errorf("%s is empty", EnvSecretKey)
	}
	path, err := secretsPath()
	if err != nil {
		return nil, "", nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		salt := make([]byte, saltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, "", nil, err
		}
		ff := &fileFormat{Version: 1, Salt: base64.StdEncoding.EncodeToString(salt), Entries: map[string]string{}}
		return ff, path, salt, nil
	}
	if err != nil {
		return nil, "", nil, err
	}
	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil, "", nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	salt, err := base64.StdEncoding.DecodeString(ff.Salt)
	if err != nil {
		return nil, "", nil, fmt.Errorf("corrupt salt in %s: %w", path, err)
	}
	if ff.Entries == nil {
		ff.Entries = map[string]string{}
	}
	return &ff, path, salt, nil
}

// save writes the envelope atomically with 0600 permissions.
func (f *File) save(ff *fileFormat, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(ff)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (f *File) deriveKey(salt []byte) []byte {
	// pbkdf2.Key only errors for an unsupported hash; SHA-256 is always fine.
	key, _ := pbkdf2.Key(sha256.New, string(f.passphrase), salt, kdfIter, keyLen)
	return key
}

// seal returns base64(nonce || AES-256-GCM ciphertext). The entry name is the
// AEAD's additional data, binding each ciphertext to its name so entries
// cannot be swapped in the file.
func seal(key []byte, name, value string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(value), []byte(name))
	return base64.StdEncoding.EncodeToString(ct), nil
}

func open(key []byte, name, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", errWrongKey
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errWrongKey
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, []byte(name))
	if err != nil {
		return "", errWrongKey
	}
	return string(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// secretsPath is $XDG_DATA_HOME/cloak/secrets.enc (or ~/.local/share/...).
func secretsPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "cloak", "secrets.enc"), nil
}
