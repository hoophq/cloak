package native

import (
	"strings"
	"testing"
)

func TestRenderLaunchd(t *testing.T) {
	p := renderLaunchd("/opt/cloak", "/tmp/cloak.log")
	for _, want := range []string{serviceLabel, "<string>/opt/cloak</string>", "<string>_daemon</string>", "RunAtLoad", "KeepAlive", "/tmp/cloak.log"} {
		if !strings.Contains(p, want) {
			t.Errorf("launchd plist missing %q:\n%s", want, p)
		}
	}
}

func TestRenderSystemd(t *testing.T) {
	u := renderSystemd("/opt/cloak")
	for _, want := range []string{"ExecStart=/opt/cloak _daemon", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("systemd unit missing %q:\n%s", want, u)
		}
	}
}

func TestPersistentMarker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if IsPersistent() {
		t.Fatal("should not be persistent initially")
	}
	if err := SetPersistent(); err != nil {
		t.Fatal(err)
	}
	if !IsPersistent() {
		t.Fatal("expected persistent after SetPersistent")
	}
	if err := ClearPersistent(); err != nil {
		t.Fatal(err)
	}
	if IsPersistent() {
		t.Fatal("expected not persistent after ClearPersistent")
	}
	if err := ClearPersistent(); err != nil { // no-op when already absent
		t.Fatalf("ClearPersistent on absent marker: %v", err)
	}
}
