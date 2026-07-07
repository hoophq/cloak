// Package token generates the fake per-session credentials handed to agents.
package token

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random session token. A fresh token is generated on every
// `cloak run`, so a token that leaks into a transcript stops working as soon
// as the session ends.
func New() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
