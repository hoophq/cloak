package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/envimport"
	"github.com/hoophq/cloak/internal/native"
	"github.com/hoophq/cloak/internal/proxy"
)

var importFlags struct {
	undo bool
	yes  bool
}

var importCmd = &cobra.Command{
	Use:   "import [file]",
	Short: "Move credentials out of a .env file into cloak",
	Long: `Scan a .env file (default: ./.env) for credentials. Postgres DSNs with
embedded passwords are imported: the password moves into cloak's secret
store, an upstream is registered, and the file entry is rewritten to a
placeholder that only works through cloak. Other credential-shaped values are
reported so you know what still leaks. The original file is backed up outside
the project tree first.`,
	Example: `  cloak import
  cloak import .env.production
  cloak import --undo .env`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().BoolVar(&importFlags.undo, "undo", false, "restore the file from its most recent backup")
	importCmd.Flags().BoolVar(&importFlags.yes, "yes", false, "skip the confirmation prompt")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	path := ".env"
	if len(args) == 1 {
		path = args[0]
	}
	out := cmd.OutOrStdout()

	if importFlags.undo {
		return undoImport(out, path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	cands, warns := envimport.Scan(lines)
	for _, w := range warns {
		fmt.Fprintf(out, "⚠ %s (line %d): %s\n", w.Key, w.LineNo+1, w.Reason)
	}
	if len(cands) == 0 {
		fmt.Fprintln(out, "no importable credentials found")
		return nil
	}

	cfg, cfgPath, err := loadConfig()
	if err != nil {
		return err
	}
	taken := func(n string) bool { _, ok := cfg.Find(n); return ok }
	for i := range cands {
		c := &cands[i]
		c.Upstream.Name = envimport.DeriveName(c.Key, taken)
		c.Upstream.ListenPort = cfg.NextListenPort()
		c.Upstream.Env = c.Key
		if err := c.Upstream.Validate(); err != nil {
			return err
		}
		// Reserves the name and port for the remaining candidates.
		cfg.Upstreams = append(cfg.Upstreams, c.Upstream)
	}

	for _, c := range cands {
		fmt.Fprintf(out, "→ %s (line %d): upstream %q on 127.0.0.1:%d, credential moves to the %s\n",
			c.Key, c.LineNo+1, c.Upstream.Name, c.Upstream.ListenPort, store.Backend())
	}
	if !importFlags.yes {
		if err := confirm(fmt.Sprintf("Rewrite %s?", path)); err != nil {
			return err
		}
	}

	// Store secrets first — a config entry must never point at a missing one.
	var stored []string
	for _, c := range cands {
		if err := store.Set(c.Upstream.Name, c.Secret); err != nil {
			for _, n := range stored {
				_ = store.Delete(n)
			}
			return fmt.Errorf("storing credential for %q: %w", c.Upstream.Name, err)
		}
		stored = append(stored, c.Upstream.Name)
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	if _, err := envimport.Backup(path); err != nil {
		return fmt.Errorf("backing up %s: %w", path, err)
	}
	tok, err := native.Token()
	if err != nil {
		return err
	}
	newLines := envimport.Rewrite(lines, cands, func(c envimport.Candidate) []string {
		return proxy.EnvAssignments(c.Upstream, tok, "")
	})
	if err := os.WriteFile(path, []byte(strings.Join(newLines, "\n")), info.Mode().Perm()); err != nil {
		return err
	}

	fmt.Fprintf(out, "✓ imported %d credential(s); %s rewritten (original backed up)\n  undo with     cloak import --undo %s\n  serve it      cloak start   (always-on; then run your app normally)\n",
		len(cands), path, path)
	return nil
}

func confirm(prompt string) error {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("aborted (no confirmation; use --yes when scripting)")
	}
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return errors.New("aborted")
	}
	return nil
}

func undoImport(out io.Writer, path string) error {
	bp, err := envimport.LatestBackup(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(bp)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ %s restored from backup\n  note: imported upstreams and their stored credentials were kept — review with `cloak list`, remove with `cloak rm <name>`\n", path)
	return nil
}
