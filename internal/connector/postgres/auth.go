package postgres

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"

	"github.com/xdg-go/scram"
)

const scramMechanism = "SCRAM-SHA-256"

// md5Response computes the legacy md5 password response:
// "md5" + hex(md5(hex(md5(password + user)) + salt)).
func md5Response(user, password string, salt [4]byte) string {
	inner := md5hex([]byte(password + user))
	outer := md5hex(append([]byte(inner), salt[:]...))
	return "md5" + outer
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// scramConversation wraps xdg-go/scram for the Postgres SASL exchange.
// Postgres ignores the SCRAM-level username (it uses the startup packet's),
// but we pass the real one through for symmetry.
type scramConversation struct {
	user     string
	password string
	conv     *scram.ClientConversation
}

func (s *scramConversation) start() ([]byte, error) {
	client, err := scram.SHA256.NewClient(s.user, s.password, "")
	if err != nil {
		return nil, fmt.Errorf("initializing SCRAM: %w", err)
	}
	s.conv = client.NewConversation()
	first, err := s.conv.Step("")
	if err != nil {
		return nil, fmt.Errorf("SCRAM client-first: %w", err)
	}
	return []byte(first), nil
}

func (s *scramConversation) step(serverData []byte) ([]byte, error) {
	if s.conv == nil {
		return nil, fmt.Errorf("SASLContinue before SASL start")
	}
	resp, err := s.conv.Step(string(serverData))
	if err != nil {
		return nil, fmt.Errorf("SCRAM client-final: %w", err)
	}
	return []byte(resp), nil
}

// finish verifies the server signature — proof the upstream really knows the
// credential, not just an impostor accepting anything.
func (s *scramConversation) finish(serverData []byte) error {
	if s.conv == nil {
		return fmt.Errorf("SASLFinal before SASL start")
	}
	if _, err := s.conv.Step(string(serverData)); err != nil {
		return fmt.Errorf("SCRAM server signature verification failed: %w", err)
	}
	return nil
}
