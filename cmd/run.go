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

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/native"
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
variables. Everything shuts down when the command exits.

If cloak is already running as a background service (cloak start), the command
reuses it instead of binding its own listeners.`,
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

	env := os.Environ()
	var mgr *proxy.Manager

	if _, up := native.DaemonPID(); up {
		// A background daemon already serves these ports; reuse it rather than
		// rebind. Inject the stable-token env so the child talks to it.
		tok, err := native.Token()
		if err != nil {
			return err
		}
		sockDir, err := native.SocketDir()
		if err != nil {
			return err
		}
		for _, u := range upstreams {
			a := proxy.EnvAssignments(u, tok, sockDir)
			logInject(u, a)
			env = append(env, a...)
		}
	} else {
		// No daemon: run an ephemeral proxy for the life of this command.
		mgr, err = proxy.New(upstreams, store)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		if err := mgr.Start(ctx); err != nil {
			return err
		}
		defer mgr.Stop()
		for _, r := range mgr.Runtimes {
			a := r.EnvAssignments()
			logInject(r.Session.Upstream, a)
			env = append(env, a...)
		}
	}

	code, err := runChild(args, env)
	if mgr != nil {
		mgr.Stop() // explicit: os.Exit below skips deferred cleanup
	}
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// logInject reports, on stderr, which variables were injected for an upstream.
func logInject(u config.Upstream, assignments []string) {
	names := make([]string, len(assignments))
	for i, kv := range assignments {
		names[i], _, _ = strings.Cut(kv, "=")
	}
	loc := fmt.Sprintf("127.0.0.1:%d", u.ListenPort)
	if u.Socket {
		loc = fmt.Sprintf("unix/%d", u.ListenPort)
	}
	fmt.Fprintf(os.Stderr, "cloak: %s → %s (%s)\n", strings.Join(names, ", "), u.Name, loc)
}

// runChild runs the wrapped command with env, returning its exit code. Ctrl+C
// reaches the child through the terminal's process group; SIGTERM is forwarded.
func runChild(args []string, env []string) (int, error) {
	child := exec.Command(args[0], args[1:]...)
	child.Env = env
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	signal.Ignore(os.Interrupt)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("starting %q: %w", strings.Join(args, " "), err)
	}
	go func() {
		for sig := range term {
			_ = child.Process.Signal(sig)
		}
	}()
	err := child.Wait()
	signal.Stop(term)
	close(term)

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}
