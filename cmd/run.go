package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/proxy"
)

var runFlags struct {
	only []string
}

var runCmd = &cobra.Command{
	Use:   "run [flags] -- <command> [args...]",
	Short: "Run a command with fake DSNs injected; proxy for the session",
	Long: `Start local listeners for the registered upstreams, mint fresh session
tokens, and run the given command with fake DSNs injected as environment
variables. Everything shuts down when the command exits.`,
	Example: `  cloak run -- claude
  cloak run --only pg-prod -- psql "$DATABASE_URL" -c 'select 1'`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRun,
}

func init() {
	runCmd.Flags().StringSliceVar(&runFlags.only, "only", nil, "comma-separated upstream names to serve (default: all)")
	runCmd.Flags().SetInterspersed(false)
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	upstreams := cfg.Upstreams
	if len(runFlags.only) > 0 {
		upstreams = nil
		for _, name := range runFlags.only {
			u, ok := cfg.Find(name)
			if !ok {
				return fmt.Errorf("no upstream named %q", name)
			}
			upstreams = append(upstreams, *u)
		}
	}
	if len(upstreams) == 0 {
		return errors.New("no upstreams registered; try `cloak add`")
	}

	mgr, err := proxy.New(upstreams, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	defer mgr.Stop()

	env := os.Environ()
	for _, r := range mgr.Runtimes {
		assignments := r.EnvAssignments()
		names := make([]string, len(assignments))
		for i, kv := range assignments {
			names[i], _, _ = strings.Cut(kv, "=")
		}
		loc := fmt.Sprintf("127.0.0.1:%d", r.Session.Upstream.ListenPort)
		if r.Session.Upstream.Socket {
			loc = fmt.Sprintf("unix/%d", r.Session.Upstream.ListenPort)
		}
		fmt.Fprintf(os.Stderr, "cloak: %s → %s (%s)\n",
			strings.Join(names, ", "), r.Session.Upstream.Name, loc)
		env = append(env, assignments...)
	}

	child := exec.Command(args[0], args[1:]...)
	child.Env = env
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	// Ctrl+C goes to the child via the terminal's process group; the proxy
	// must stay alive until the child decides to exit. SIGTERM is forwarded.
	signal.Ignore(os.Interrupt)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	if err := child.Start(); err != nil {
		return fmt.Errorf("starting %q: %w", strings.Join(args, " "), err)
	}
	go func() {
		for sig := range term {
			_ = child.Process.Signal(sig)
		}
	}()
	err = child.Wait()
	signal.Stop(term)
	close(term)

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		mgr.Stop()
		os.Exit(exitErr.ExitCode())
	}
	return err
}
