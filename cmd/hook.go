package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hoophq/cloak/internal/native"
	"github.com/hoophq/cloak/internal/proxy"
)

// _daemon is the background proxy behind `cloak init`: one per machine, holding
// an exclusive lock, serving the fixed loopback listeners until signalled.
var daemonCmd = &cobra.Command{
	Use:    "_daemon",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runDaemon,
}

// _hook is invoked by Claude Code's SessionStart / SessionEnd hooks; it reads
// the hook payload (session_id) on stdin and starts/stops the daemon.
var hookCmd = &cobra.Command{
	Use:       "_hook",
	Hidden:    true,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"session-start", "session-end"},
	RunE:      runHook,
}

func init() {
	rootCmd.AddCommand(daemonCmd, hookCmd)
}

func runDaemon(cmd *cobra.Command, args []string) error {
	// Only one daemon runs; a second invocation exits quietly.
	release, ok, err := native.Lock()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer release()

	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Upstreams) == 0 {
		return errors.New("no upstreams registered")
	}
	tok, err := native.Token()
	if err != nil {
		return err
	}
	sockDir, err := native.SocketDir()
	if err != nil {
		return err
	}

	mgr, err := proxy.NewFixed(cfg.Upstreams, store, tok, sockDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	defer mgr.Stop()

	if err := native.WritePID(); err != nil {
		return err
	}
	defer native.RemovePID()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	return nil
}

func runHook(cmd *cobra.Command, args []string) error {
	// Best-effort: the hook must never break a session, so a bad payload just
	// falls back to a single default session id.
	var in struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	id := in.SessionID
	if id == "" {
		id = "default"
	}

	switch args[0] {
	case "session-start":
		if err := native.AddSession(id); err != nil {
			fmt.Fprintf(os.Stderr, "cloak: %v\n", err)
		}
		if err := native.EnsureDaemon(); err != nil {
			// Fail open (never block the session), but say so loudly: this
			// session is running without cloak's protection.
			fmt.Fprintf(os.Stderr, "cloak: %v\n", err)
			systemMessage(cmd, "⚠️ cloak could not start its proxy — this session is NOT protected (see cloak's daemon log)")
			return nil
		}
		emitBanner(cmd)
	case "session-end":
		_ = native.RemoveSession(id)
		if native.IsPersistent() {
			return nil // an always-on daemon (cloak start) must outlive the session
		}
		if n, err := native.SessionCount(); err == nil && n == 0 {
			_ = native.StopDaemon()
		}
	default:
		return fmt.Errorf("unknown hook %q", args[0])
	}
	return nil
}

// systemMessage emits a one-line chat notice (like fence's) to the user,
// without adding anything to the model's context.
func systemMessage(cmd *cobra.Command, msg string) {
	_ = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
		SystemMessage string `json:"systemMessage"`
	}{msg})
}

// emitBanner confirms cloak is active and names the upstreams it covers.
func emitBanner(cmd *cobra.Command) {
	cfg, _, err := loadConfig()
	if err != nil || len(cfg.Upstreams) == 0 {
		return
	}
	names := make([]string, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		names[i] = u.Name
	}
	systemMessage(cmd, fmt.Sprintf("🔒 cloak is proxying %s — real credentials stay out of this session", strings.Join(names, ", ")))
}
