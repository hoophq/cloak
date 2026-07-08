// Package secret stores real credentials. The default backend is the OS
// keychain; a passphrase-encrypted file backend ([File]) is the fallback for
// headless hosts. A secret is never written to disk in plaintext, nor to logs.
package secret

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// service is the keychain service name all Cloak entries live under.
const service = "cloak"

// ErrNotFound is returned when no credential is stored for an upstream.
var ErrNotFound = errors.New("credential not found")

type Store interface {
	Set(name, value string) error
	Get(name string) (string, error)
	Delete(name string) error
	// Backend names where credentials are kept, for user-facing messages.
	Backend() string
}

// Default selects the backend: the encrypted-file backend when EnvSecretKey is
// set (the headless / CI opt-in), otherwise the OS keychain.
func Default() Store {
	if key := os.Getenv(EnvSecretKey); key != "" {
		return NewFile(key)
	}
	return Keyring{}
}

// Keyring stores credentials in the OS keychain (macOS Keychain, Linux
// secret-service, Windows credential manager).
type Keyring struct{}

func (Keyring) Backend() string { return "OS keychain" }

func (Keyring) Set(name, value string) error {
	if err := keyring.Set(service, name, value); err != nil {
		return fmt.Errorf("%w (no OS keychain available? set %s to use the encrypted-file backend)", err, EnvSecretKey)
	}
	return nil
}

func (Keyring) Get(name string) (string, error) {
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("%w for upstream %q (re-run `cloak add %s`)", ErrNotFound, name, name)
	}
	return v, err
}

func (Keyring) Delete(name string) error {
	err := keyring.Delete(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Mem is an in-memory store for tests.
type Mem map[string]string

func (Mem) Backend() string { return "memory" }

func (m Mem) Set(name, value string) error { m[name] = value; return nil }

func (m Mem) Get(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%w for upstream %q", ErrNotFound, name)
	}
	return v, nil
}

func (m Mem) Delete(name string) error { delete(m, name); return nil }
