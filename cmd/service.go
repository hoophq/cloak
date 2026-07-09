package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/native"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Run cloak's proxy as an always-on background service (starts at login)",
	Long: `Install and start cloak as a login service (launchd on macOS, systemd --user
on Linux). The proxy stays up across shell sessions and reboots, so any app
whose credentials you have moved into cloak keeps working when run normally —
no "cloak run" wrapper. Stop and remove it with "cloak stop".`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		if len(cfg.Upstreams) == 0 {
			return errors.New("no upstreams registered; run `cloak add` first")
		}
		// Fail fast if a credential is missing, rather than in the service.
		for _, u := range cfg.Upstreams {
			if _, err := store.Get(u.Name); err != nil {
				return fmt.Errorf("upstream %q: %w", u.Name, err)
			}
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		if err := native.SetPersistent(); err != nil {
			return err
		}
		if err := native.InstallService(self); err != nil {
			_ = native.ClearPersistent()
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"✓ cloak is running as a background service, proxying %d upstream(s)\n"+
				"  it starts at login and stays up; check it with `cloak status`, stop with `cloak stop`.\n",
			len(cfg.Upstreams))
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the cloak proxy is running and what it serves",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		pid, up := native.DaemonPID()
		switch {
		case up && native.IsPersistent():
			fmt.Fprintf(out, "cloak: running — always-on service (pid %d)\n", pid)
		case up:
			fmt.Fprintf(out, "cloak: running — session proxy (pid %d)\n", pid)
		default:
			fmt.Fprintln(out, "cloak: not running (start it with `cloak start`, or run through `cloak run`)")
		}
		if cfg, _, err := loadConfig(); err == nil {
			for _, u := range cfg.Upstreams {
				fmt.Fprintf(out, "  %s → 127.0.0.1:%d\n", u.Name, u.ListenPort)
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(startCmd, statusCmd)
}
