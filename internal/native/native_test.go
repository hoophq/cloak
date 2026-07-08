package native

import (
	"testing"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func TestTokenIsStable(t *testing.T) {
	isolate(t)
	a, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 {
		t.Fatalf("token = %q, want 16 hex chars", a)
	}
	b, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("token not stable: %q != %q", a, b)
	}
}

func TestSessionBookkeeping(t *testing.T) {
	isolate(t)
	if n, err := SessionCount(); err != nil || n != 0 {
		t.Fatalf("initial count = %d, %v", n, err)
	}
	if err := AddSession("s1"); err != nil {
		t.Fatal(err)
	}
	if err := AddSession("s2"); err != nil {
		t.Fatal(err)
	}
	if err := AddSession("s1"); err != nil { // idempotent
		t.Fatal(err)
	}
	if n, _ := SessionCount(); n != 2 {
		t.Fatalf("count after adds = %d, want 2", n)
	}
	if err := RemoveSession("s1"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSession("s1"); err != nil { // no error when already gone
		t.Fatal(err)
	}
	if n, _ := SessionCount(); n != 1 {
		t.Fatalf("count after remove = %d, want 1", n)
	}
	if err := ClearSessions(); err != nil {
		t.Fatal(err)
	}
	if n, _ := SessionCount(); n != 0 {
		t.Fatalf("count after clear = %d, want 0", n)
	}
}

func TestDaemonPIDNoneWhenAbsent(t *testing.T) {
	isolate(t)
	if pid, ok := DaemonPID(); ok {
		t.Fatalf("expected no daemon, got pid %d", pid)
	}
	// StopDaemon is a no-op with nothing running.
	if err := StopDaemon(); err != nil {
		t.Fatal(err)
	}
}

func TestLockExcludesSecond(t *testing.T) {
	isolate(t)
	release, ok, err := Lock()
	if err != nil || !ok {
		t.Fatalf("first Lock: ok=%v err=%v", ok, err)
	}
	_, ok2, err := Lock()
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Fatal("second Lock should not have been acquired while the first is held")
	}
	release()
	release2, ok3, err := Lock()
	if err != nil || !ok3 {
		t.Fatalf("Lock after release: ok=%v err=%v", ok3, err)
	}
	release2()
}
