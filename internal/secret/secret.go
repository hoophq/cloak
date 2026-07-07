// Package secret stores real credentials. The default backend is the OS
// keychain; nothing in this package ever writes a secret to disk or to logs.
package secret

import (
	"errors"
	"fmt"

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
}

// Keyring stores credentials in the OS keychain (macOS Keychain, Linux
// secret-service, Windows credential manager).
type Keyring struct{}

func (Keyring) Set(name, value string) error {
	return keyring.Set(service, name, value)
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

func (m Mem) Set(name, value string) error { m[name] = value; return nil }

func (m Mem) Get(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%w for upstream %q", ErrNotFound, name)
	}
	return v, nil
}

func (m Mem) Delete(name string) error { delete(m, name); return nil }
