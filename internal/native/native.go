// Package native backs cloak's wrapper-free Claude Code integration: a stable
// per-machine fake token, a session-scoped background proxy (the daemon), and
// the session bookkeeping that starts it when a Claude session opens and stops
// it when the last one closes. Unix only (macOS / Linux).
package native

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoophq/cloak/internal/token"
)

// --- paths ----------------------------------------------------------------

// stateDir is $XDG_STATE_HOME/cloak (or ~/.local/state/cloak): durable state
// such as the session token.
func stateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "cloak")
	return dir, os.MkdirAll(dir, 0o700)
}

// runtimeDir is $XDG_RUNTIME_DIR/cloak (or <state>/run): ephemeral files —
// pidfile, session markers, unix sockets.
func runtimeDir() (string, error) {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		dir := filepath.Join(base, "cloak")
		return dir, os.MkdirAll(dir, 0o700)
	}
	st, err := stateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(st, "run")
	return dir, os.MkdirAll(dir, 0o700)
}

// SocketDir is the deterministic directory unix-socket upstreams bind in, known
// to both `cloak init` (which writes the fake DSNs) and the daemon.
func SocketDir() (string, error) {
	rt, err := runtimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(rt, "sockets"), nil
}

// Token returns the stable per-machine fake session token, generating and
// persisting it (0600) on first use. It is fake — the value the agent sees in
// place of a real credential — not a secret.
func Token() (string, error) {
	st, err := stateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(st, "session-token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	t, err := token.New()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

func pidPath() (string, error) {
	rt, err := runtimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(rt, "daemon.pid"), nil
}

func lockPath() (string, error) {
	rt, err := runtimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(rt, "daemon.lock"), nil
}

func sessionsDir() (string, error) {
	rt, err := runtimeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(rt, "sessions")
	return dir, os.MkdirAll(dir, 0o700)
}

// --- session bookkeeping --------------------------------------------------

// AddSession records a live Claude session by id (a marker file). Concurrent
// sessions each hold their own marker.
func AddSession(id string) error {
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sanitize(id)), []byte(strconv.Itoa(os.Getppid())), 0o600)
}

// RemoveSession drops a session's marker (no error if already gone).
func RemoveSession(id string) error {
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, sanitize(id)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SessionCount is how many Claude sessions are currently open.
func SessionCount() (int, error) {
	dir, err := sessionsDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// ClearSessions removes all markers — the escape hatch for `cloak stop`.
func ClearSessions() error {
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// --- daemon lifecycle -----------------------------------------------------

// DaemonPID returns the running daemon's pid, or (0, false) if none is alive.
func DaemonPID() (int, bool) {
	path, err := pidPath()
	if err != nil {
		return 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if syscall.Kill(pid, 0) != nil { // ESRCH => not running
		return 0, false
	}
	return pid, true
}

// EnsureDaemon starts the background proxy if it is not already running, then
// waits briefly for it to come up. Extra invocations are harmless: the daemon
// self-serializes with an exclusive lock and a second one exits immediately.
func EnsureDaemon() error {
	if _, ok := DaemonPID(); ok {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	st, err := stateDir()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(st, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()

	cmd := &os.ProcAttr{
		Files: []*os.File{nil, logf, logf},
		Sys:   &syscall.SysProcAttr{Setsid: true}, // detach so it outlives this hook
	}
	proc, err := os.StartProcess(self, []string{self, "_daemon"}, cmd)
	if err != nil {
		return fmt.Errorf("starting cloak daemon: %w", err)
	}
	_ = proc.Release()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := DaemonPID(); ok {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return fmt.Errorf("cloak daemon did not come up (see %s/daemon.log)", st)
}

// StopDaemon signals the running daemon to exit and removes its pidfile.
func StopDaemon() error {
	pid, ok := DaemonPID()
	if !ok {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	path, _ := pidPath()
	_ = os.Remove(path)
	return nil
}

// Lock is held by the daemon for its lifetime, so only one daemon runs. ok is
// false when another daemon already holds it (this one should exit quietly).
func Lock() (release func(), ok bool, err error) {
	path, err := lockPath()
	if err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false, nil // already locked by a live daemon
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true, nil
}

// WritePID / RemovePID record the daemon's own pid for EnsureDaemon/StopDaemon.
func WritePID() error {
	path, err := pidPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

func RemovePID() {
	if path, err := pidPath(); err == nil {
		_ = os.Remove(path)
	}
}
