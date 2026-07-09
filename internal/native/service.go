package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// serviceLabel identifies cloak's OS service (launchd label / systemd unit).
const serviceLabel = "dev.hoop.cloak"

// renderLaunchd returns the launchd agent plist that runs the daemon at login
// and restarts it on exit.
func renderLaunchd(cloakPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>_daemon</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, serviceLabel, cloakPath, logPath, logPath)
}

// renderSystemd returns the systemd --user unit that runs the daemon and
// restarts it on failure.
func renderSystemd(cloakPath string) string {
	return fmt.Sprintf(`[Unit]
Description=cloak credential proxy

[Service]
ExecStart=%s _daemon
Restart=on-failure

[Install]
WantedBy=default.target
`, cloakPath)
}

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
}

func systemdUnitPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", "cloak.service"), nil
}

func serviceUnitPath() (string, error) {
	if runtime.GOOS == "darwin" {
		return launchdPlistPath()
	}
	return systemdUnitPath()
}

// ServiceInstalled reports whether cloak's OS service unit is present.
func ServiceInstalled() bool {
	path, err := serviceUnitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// InstallService writes and loads the OS service so the daemon runs at login
// and is kept alive. cloakPath is the absolute cloak binary.
func InstallService(cloakPath string) error {
	path, err := serviceUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		st, err := stateDir()
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(renderLaunchd(cloakPath, filepath.Join(st, "daemon.log"))), 0o644); err != nil {
			return err
		}
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel)
		_ = exec.Command("launchctl", "bootout", target).Run() // clear a stale load
		if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
		}
		return nil
	case "linux":
		if err := os.WriteFile(path, []byte(renderSystemd(cloakPath)), 0o644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "cloak.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable: %w: %s", err, out)
		}
		return nil
	default:
		return fmt.Errorf("cloak start is not supported on %s", runtime.GOOS)
	}
}

// UninstallService stops and removes the OS service. Missing is not an error.
func UninstallService() error {
	path, err := serviceUnitPath()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel)).Run()
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "cloak.service").Run()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return nil
}

// --- persistent marker ----------------------------------------------------
//
// The marker records that the daemon is meant to be always-on (via `cloak
// start`), so the Claude Code SessionEnd hook does not stop it.

func persistentPath() (string, error) {
	st, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(st, "persistent"), nil
}

// SetPersistent marks the daemon as always-on.
func SetPersistent() error {
	path, err := persistentPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// ClearPersistent removes the always-on marker.
func ClearPersistent() error {
	path, err := persistentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsPersistent reports whether the daemon is marked always-on.
func IsPersistent() bool {
	path, err := persistentPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
