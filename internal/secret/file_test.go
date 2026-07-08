package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Keep the KDF cheap so a test doing several ops does not spend seconds in
// PBKDF2. Production uses the full count.
func init() { kdfIter = 4096 }

func newStore(t *testing.T, passphrase string) *File {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return NewFile(passphrase)
}

func TestFileRoundTrip(t *testing.T) {
	s := newStore(t, "correct horse battery staple")
	if err := s.Set("pg-prod", "s3cr3t-pw"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("pg-prod")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t-pw" {
		t.Fatalf("Get = %q, want the stored secret", got)
	}
}

func TestFilePersistsAcrossInstances(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := NewFile("pass").Set("api", "key-123"); err != nil {
		t.Fatal(err)
	}
	// A brand-new File with the same passphrase must read the earlier write.
	got, err := NewFile("pass").Get("api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "key-123" {
		t.Fatalf("Get across instances = %q", got)
	}
}

func TestFileWrongPassphraseFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := NewFile("right").Set("api", "key-123"); err != nil {
		t.Fatal(err)
	}
	got, err := NewFile("wrong").Get("api")
	if err == nil {
		t.Fatalf("wrong passphrase returned %q, want error", got)
	}
	if !errors.Is(err, errWrongKey) {
		t.Fatalf("err = %v, want errWrongKey", err)
	}
}

func TestFileMissingEntry(t *testing.T) {
	s := newStore(t, "pass")
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing err = %v, want ErrNotFound", err)
	}
}

func TestFileDelete(t *testing.T) {
	s := newStore(t, "pass")
	if err := s.Set("api", "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("api"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("api"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete, Get err = %v, want ErrNotFound", err)
	}
	// Deleting a missing entry is a no-op, not an error.
	if err := s.Delete("api"); err != nil {
		t.Fatalf("Delete missing = %v, want nil", err)
	}
}

func TestFilePermissionsAndNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	const pw = "super-secret-value-not-in-file"
	if err := NewFile("pass").Set("api", pw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cloak", "secrets.enc")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(pw)) {
		t.Fatal("plaintext secret found in the store file")
	}
}

func TestFileTamperDetected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if err := NewFile("pass").Set("api", "key-123"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cloak", "secrets.enc")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the stored ciphertext; GCM must reject it.
	ct := []byte(ff.Entries["api"])
	ct[len(ct)-1] ^= 0x01
	ff.Entries["api"] = string(ct)
	out, err := json.Marshal(ff)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile("pass").Get("api"); !errors.Is(err, errWrongKey) {
		t.Fatalf("tampered Get err = %v, want errWrongKey", err)
	}
}

func TestDefaultSelectsFileBackend(t *testing.T) {
	t.Setenv(EnvSecretKey, "pass")
	if _, ok := Default().(*File); !ok {
		t.Fatalf("Default() = %T, want *File when %s is set", Default(), EnvSecretKey)
	}
	t.Setenv(EnvSecretKey, "")
	if _, ok := Default().(Keyring); !ok {
		t.Fatalf("Default() = %T, want Keyring when %s is unset", Default(), EnvSecretKey)
	}
}
