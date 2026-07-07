package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered upstreams (never shows credentials)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := loadConfig()
		if err != nil {
			return err
		}
		if len(cfg.Upstreams) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no upstreams registered; try `cloak add`")
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tUPSTREAM\tDB\tLOCAL\tENV\tTLS")
		for _, u := range cfg.Upstreams {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t127.0.0.1:%d\t%s\t%s\n",
				u.Name, u.Type, u.Addr(), u.DBName(), u.ListenPort, u.Env, u.TLS)
		}
		return w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
