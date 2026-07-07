// Package cmd implements the cloak CLI.
package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/secret"
)

var (
	flagConfig string

	// store is swappable for tests.
	store secret.Store = secret.Keyring{}
)

var rootCmd = &cobra.Command{
	Use:   "cloak",
	Short: "Hand agents fake credentials; keep the real ones out of their context",
	Long: `Cloak is a local credential proxy. Register an upstream once, then run
your agent through it: the agent gets a fake DSN pointing at localhost and
Cloak swaps in the real credential on the way out. The real secret never
enters the agent's context window, logs, or traces.`,
	SilenceUsage: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "config file (default $XDG_CONFIG_HOME/cloak/config.yaml)")
}

func configPath() (string, error) {
	if flagConfig != "" {
		return flagConfig, nil
	}
	return config.Path()
}

func loadConfig() (*config.Config, string, error) {
	path, err := configPath()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	return cfg, path, err
}
