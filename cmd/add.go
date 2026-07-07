package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hoophq/cloak/internal/config"
)

var addFlags struct {
	typ           string
	url           string
	host          string
	port          int
	db            string
	user          string
	env           string
	listenPort    int
	tls           string
	passwordStdin bool
}

var addCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register an upstream; the credential goes to the OS keychain",
	Long: `Register an upstream service. You are prompted for the password, which is
stored in the OS keychain — never in a file, never as a command-line argument.`,
	Example: `  cloak add pg-prod --host prod-db.internal --user app_user --db app --env DATABASE_URL
  cloak add pg-prod --url postgres://app_user@prod-db.internal:5432/app --env DATABASE_URL`,
	Args: cobra.ExactArgs(1),
	RunE: runAdd,
}

func init() {
	f := addCmd.Flags()
	f.StringVar(&addFlags.typ, "type", config.TypePostgres, "upstream type (postgres)")
	f.StringVar(&addFlags.url, "url", "", "upstream URL, e.g. postgres://user@host:5432/db (no password!)")
	f.StringVar(&addFlags.host, "host", "", "upstream host")
	f.IntVar(&addFlags.port, "port", 5432, "upstream port")
	f.StringVar(&addFlags.db, "db", "", "database name (default: same as user)")
	f.StringVar(&addFlags.user, "user", "", "real upstream username")
	f.StringVar(&addFlags.env, "env", "", "env var to inject the fake DSN as during `cloak run` (e.g. DATABASE_URL)")
	f.IntVar(&addFlags.listenPort, "listen-port", 0, "local listener port (default: auto, starting at 5433)")
	f.StringVar(&addFlags.tls, "tls", config.TLSVerifyFull, "upstream TLS mode: verify-full or disable (local dev only)")
	f.BoolVar(&addFlags.passwordStdin, "password-stdin", false, "read the password from stdin instead of prompting")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, path, err := loadConfig()
	if err != nil {
		return err
	}
	if _, exists := cfg.Find(name); exists {
		return fmt.Errorf("upstream %q already exists; `cloak rm %s` first", name, name)
	}

	if addFlags.url != "" {
		if err := applyURL(addFlags.url); err != nil {
			return err
		}
	}
	listenPort := addFlags.listenPort
	if listenPort == 0 {
		listenPort = cfg.NextListenPort()
	}
	env := addFlags.env
	if env == "" {
		env = defaultEnvName(name)
	}
	u := config.Upstream{
		Name:       name,
		Type:       addFlags.typ,
		Host:       addFlags.host,
		Port:       addFlags.port,
		Database:   addFlags.db,
		User:       addFlags.user,
		ListenPort: listenPort,
		Env:        env,
		TLS:        addFlags.tls,
	}
	if err := u.Validate(); err != nil {
		return err
	}

	password, err := readPassword(u.User, u.Host)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("empty password")
	}

	// Keychain first: if it fails, no config entry points at a missing secret.
	if err := store.Set(name, password); err != nil {
		return fmt.Errorf("storing credential in keychain: %w", err)
	}
	cfg.Upstreams = append(cfg.Upstreams, u)
	if err := cfg.Save(path); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ %s registered (credential in OS keychain)\n  local listener  127.0.0.1:%d\n  injected as     %s\n  try it          cloak run -- psql \"$%s\" -c 'select 1'\n",
		name, listenPort, env, env)
	return nil
}

// applyURL fills the host/port/db/user flags from --url. Passwords in URLs
// are rejected outright: they land in argv, shell history, and transcripts.
func applyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing --url: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("--url must be a postgres:// URL")
	}
	if _, has := u.User.Password(); has {
		return fmt.Errorf("--url contains a password: never pass secrets in URLs or argv — you will be prompted instead")
	}
	if h := u.Hostname(); h != "" {
		addFlags.host = h
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("invalid port in --url: %q", p)
		}
		addFlags.port = n
	}
	if u.User != nil && u.User.Username() != "" {
		addFlags.user = u.User.Username()
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		addFlags.db = db
	}
	return nil
}

func defaultEnvName(name string) string {
	s := strings.ToUpper(name)
	s = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
	return "CLOAK_" + s + "_URL"
}

func readPassword(user, host string) (string, error) {
	if addFlags.passwordStdin {
		data, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("no terminal for password prompt (use --password-stdin when scripting): %w", err)
	}
	defer tty.Close()
	fmt.Fprintf(os.Stderr, "Password for %s@%s: ", user, host)
	pw, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}
