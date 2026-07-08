package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove an upstream and its stored credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, path, err := loadConfig()
		if err != nil {
			return err
		}
		if !cfg.Remove(name) {
			return fmt.Errorf("no upstream named %q", name)
		}
		if err := store.Delete(name); err != nil {
			return fmt.Errorf("removing stored credential: %w", err)
		}
		if err := cfg.Save(path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s removed\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rmCmd)
}
