package token

import "testing"

func TestNew(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 {
		t.Fatalf("token length = %d, want 16", len(a))
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two tokens should not collide")
	}
}
