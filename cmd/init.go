package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/claudecfg"
	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/native"
	"github.com/hoophq/cloak/internal/proxy"
)

var nativeFlags struct{ project bool }

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Wire cloak into Claude Code so plain `claude` runs through it (no `cloak run`)",
	Long: `Write cloak's fake credentials and session hooks into Claude Code's
settings.json. After this, starting a Claude Code session with plain "claude"
runs it through cloak automatically — the real credentials stay in the keychain
and out of the agent's context. Preserves your other settings; undo with
"cloak uninstall".`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove cloak's Claude Code integration (added by `cloak init`)",
	Args:  cobra.NoArgs,
	RunE:  runUninstall,
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the cloak background proxy (and any always-on service)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = native.ClearPersistent()
		if err := native.UninstallService(); err != nil {
			return err
		}
		_ = native.ClearSessions()
		if err := native.StopDaemon(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "✓ cloak proxy stopped")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&nativeFlags.project, "project", false, "write ./.claude/settings.json instead of the global ~/.claude/settings.json")
	uninstallCmd.Flags().BoolVar(&nativeFlags.project, "project", false, "target ./.claude/settings.json instead of the global one")
	rootCmd.AddCommand(initCmd, uninstallCmd, stopCmd)
}

func settingsPath(project bool) (string, error) {
	if project {
		return filepath.Join(".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func runInit(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Upstreams) == 0 {
		return errors.New("no upstreams registered; run `cloak add` first")
	}
	// Fail fast if a credential is missing, rather than in the detached daemon.
	for _, u := range cfg.Upstreams {
		if _, err := store.Get(u.Name); err != nil {
			return fmt.Errorf("upstream %q: %w", u.Name, err)
		}
	}

	env, err := managedEnv(cfg)
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := settingsPath(nativeFlags.project)
	if err != nil {
		return err
	}

	managed, skipped, err := claudecfg.Install(path, claudecfg.Managed{Env: env, HookCommand: self + " _hook"})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "✓ cloak wired into Claude Code (%s)\n", path)
	fmt.Fprintf(out, "  managing %d variable(s): %s\n", len(managed), strings.Join(managed, ", "))
	for _, k := range skipped {
		fmt.Fprintf(out, "  ⚠ %s already set to a non-cloak value — left as-is; remove it and re-run to let cloak manage it\n", k)
	}
	fmt.Fprintln(out, "  start a new Claude Code session (`claude`) and cloak is on — no `cloak run` needed.")
	return nil
}

func runUninstall(cmd *cobra.Command, args []string) error {
	path, err := settingsPath(nativeFlags.project)
	if err != nil {
		return err
	}
	if err := claudecfg.Uninstall(path); err != nil {
		return err
	}
	_ = native.ClearSessions()
	_ = native.StopDaemon()
	fmt.Fprintf(cmd.OutOrStdout(), "✓ cloak removed from Claude Code (%s); proxy stopped\n", path)
	return nil
}

// managedEnv builds the fake env values cloak injects into Claude Code — one
// entry per upstream variable (DSN, key, base URL) — using the stable daemon
// token so the values match what the daemon serves.
func managedEnv(cfg *config.Config) (map[string]string, error) {
	tok, err := native.Token()
	if err != nil {
		return nil, err
	}
	sockDir, err := native.SocketDir()
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, u := range cfg.Upstreams {
		for _, kv := range proxy.EnvAssignments(u, tok, sockDir) {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	return env, nil
}

// syncClaude rewrites cloak's managed env + hooks into every settings.json that
// already has the integration (the global file and, when present, the project
// one), so a credential added after `cloak init` is picked up without a manual
// re-init. Files where cloak was never installed are left untouched — this
// never opts a user in. It returns the paths it refreshed.
func syncClaude(cfg *config.Config) ([]string, error) {
	if len(cfg.Upstreams) == 0 {
		return nil, nil
	}
	env, err := managedEnv(cfg)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var refreshed []string
	seen := map[string]bool{}
	for _, project := range []bool{false, true} {
		path, err := settingsPath(project)
		if err != nil {
			return refreshed, err
		}
		if abs, err := filepath.Abs(path); err == nil {
			if seen[abs] {
				continue
			}
			seen[abs] = true
		}
		installed, err := claudecfg.Installed(path)
		if err != nil {
			return refreshed, err
		}
		if !installed {
			continue
		}
		if _, _, err := claudecfg.Install(path, claudecfg.Managed{Env: env, HookCommand: self + " _hook"}); err != nil {
			return refreshed, err
		}
		refreshed = append(refreshed, path)
	}
	return refreshed, nil
}

// resyncAfterChange propagates a just-registered credential to whatever is
// already running, so the user need not re-run `cloak init` or restart the
// daemon: it reloads a live daemon and refreshes any installed Claude Code
// settings. Best-effort — it reports what it did and stays quiet when there is
// nothing to do.
func resyncAfterChange(out io.Writer, cfg *config.Config) {
	if reloaded, err := native.ReloadDaemon(); err != nil {
		fmt.Fprintf(out, "  ⚠ could not reload the running cloak daemon: %v\n", err)
	} else if reloaded {
		fmt.Fprintln(out, "  ↻ reloaded the running cloak daemon")
	}
	refreshed, err := syncClaude(cfg)
	if err != nil {
		fmt.Fprintf(out, "  ⚠ could not resync Claude Code settings: %v\n", err)
		return
	}
	for _, p := range refreshed {
		fmt.Fprintf(out, "  ↻ resynced Claude Code (%s)\n", p)
	}
}
