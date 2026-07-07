package postgres

import (
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/xdg-go/scram"
)

func TestMD5Response(t *testing.T) {
	// Reconstruct the documented algorithm explicitly to guard the
	// concatenation order: md5(md5(password + user) + salt).
	user, password := "app_user", "s3cret"
	salt := [4]byte{0xde, 0xad, 0xbe, 0xef}

	h1 := md5.Sum([]byte(password + user))
	inner := hex.EncodeToString(h1[:])
	h2 := md5.Sum(append([]byte(inner), salt[:]...))
	want := "md5" + hex.EncodeToString(h2[:])

	if got := md5Response(user, password, salt); got != want {
		t.Fatalf("md5Response = %q, want %q", got, want)
	}
}

// TestScramConversation runs the client glue against xdg-go's server
// implementation to validate the full exchange including server-signature
// verification.
func TestScramConversation(t *testing.T) {
	const user, password = "e2euser", "pw-123"

	cred, err := scram.SHA256.NewClient(user, password, "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := cred.GetStoredCredentialsWithError(scram.KeyFactors{Salt: "0123456789abcdef", Iters: 4096})
	if err != nil {
		t.Fatal(err)
	}
	server, err := scram.SHA256.NewServer(func(string) (scram.StoredCredentials, error) {
		return stored, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	serverConv := server.NewConversation()

	sc := &scramConversation{user: user, password: password}
	first, err := sc.start()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := serverConv.Step(string(first))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sc.step([]byte(challenge))
	if err != nil {
		t.Fatal(err)
	}
	final, err := serverConv.Step(string(resp))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.finish([]byte(final)); err != nil {
		t.Fatal(err)
	}
	if !serverConv.Valid() {
		t.Fatal("server did not accept the client proof")
	}
}

func TestScramRejectsBadServerSignature(t *testing.T) {
	sc := &scramConversation{user: "u", password: "pw"}
	if _, err := sc.start(); err != nil {
		t.Fatal(err)
	}
	if err := sc.finish([]byte("v=Zm9yZ2VkLXNpZ25hdHVyZQ==")); err == nil {
		t.Fatal("expected forged server signature to be rejected")
	}
}
