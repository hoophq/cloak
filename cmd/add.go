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
	auth          string
	env           string
	envURL        string
	listenPort    int
	tls           string
	socket        bool
	passwordStdin bool
}

var addCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register an upstream; the credential goes to the OS keychain",
	Long: `Register an upstream service. You are prompted for the credential, which is
stored in the OS keychain — never in a file, never as a command-line argument.`,
	Example: `  cloak add pg-prod --host prod-db.internal --user app_user --db app --env DATABASE_URL
  cloak add pg-prod --url postgres://app_user@prod-db.internal:5432/app --env DATABASE_URL
  cloak add openai --type http --host api.openai.com --auth bearer --env OPENAI_API_KEY --env-url OPENAI_BASE_URL
  cloak add anthropic --type http --host api.anthropic.com --auth header:x-api-key --env ANTHROPIC_API_KEY --env-url ANTHROPIC_BASE_URL`,
	Args: cobra.ExactArgs(1),
	RunE: runAdd,
}

func init() {
	f := addCmd.Flags()
	f.StringVar(&addFlags.typ, "type", config.TypePostgres, "upstream type: postgres or http")
	f.StringVar(&addFlags.url, "url", "", "upstream URL, e.g. postgres://user@host:5432/db (no password!)")
	f.StringVar(&addFlags.host, "host", "", "upstream host")
	f.IntVar(&addFlags.port, "port", 0, "upstream port (default: 5432 for postgres, 443 for http)")
	f.StringVar(&addFlags.db, "db", "", "postgres: database name (default: same as user)")
	f.StringVar(&addFlags.user, "user", "", "postgres: real upstream username")
	f.StringVar(&addFlags.auth, "auth", "", "http: credential placement, bearer or header:<name>")
	f.StringVar(&addFlags.env, "env", "", "env var to inject the fake DSN/key as during `cloak run`")
	f.StringVar(&addFlags.envURL, "env-url", "", "http: env var to inject the local base URL as")
	f.IntVar(&addFlags.listenPort, "listen-port", 0, "local listener port (default: auto, starting at 5433)")
	f.StringVar(&addFlags.tls, "tls", config.TLSVerifyFull, "upstream TLS mode: verify-full or disable (local dev only)")
	f.BoolVar(&addFlags.socket, "socket", false, "postgres: serve on a unix-domain socket (restricted to your user by filesystem permissions) instead of loopback TCP")
	f.BoolVar(&addFlags.passwordStdin, "password-stdin", false, "read the credential from stdin instead of prompting")
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
	u := config.Upstream{
		Name:       name,
		Type:       addFlags.typ,
		Host:       addFlags.host,
		Port:       addFlags.port,
		Database:   addFlags.db,
		User:       addFlags.user,
		Socket:     addFlags.socket,
		Auth:       addFlags.auth,
		ListenPort: listenPort,
		Env:        addFlags.env,
		EnvURL:     addFlags.envURL,
		TLS:        addFlags.tls,
	}
	applyTypeDefaults(&u)
	if err := u.Validate(); err != nil {
		return err
	}

	password, err := readPassword(promptLabel(u))
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("empty credential")
	}

	// Store the secret first: if it fails, no config entry points at a
	// missing secret.
	if err := store.Set(name, password); err != nil {
		return fmt.Errorf("storing credential: %w", err)
	}
	cfg.Upstreams = append(cfg.Upstreams, u)
	if err := cfg.Save(path); err != nil {
		return err
	}

	tryIt := fmt.Sprintf("cloak run -- psql \"$%s\" -c 'select 1'", u.Env)
	injected := u.Env
	if u.Type == config.TypeHTTP {
		injected = u.Env + ", " + u.EnvURL
		// Match the example to the upstream's actual auth placement.
		header, prefix, _ := config.ParseAuth(u.Auth)
		tryIt = fmt.Sprintf("cloak run -- sh -c 'curl -H \"%s: %s$%s\" \"$%s/\"'", header, prefix, u.Env, u.EnvURL)
	}
	local := fmt.Sprintf("127.0.0.1:%d", listenPort)
	if u.Socket {
		local = fmt.Sprintf("unix socket (.s.PGSQL.%d), restricted to your user", listenPort)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ %s registered (credential in %s)\n  local listener  %s\n  injected as     %s\n  try it          %s\n",
		name, store.Backend(), local, injected, tryIt)
	return nil
}

// applyTypeDefaults fills the per-type defaults for flags the user omitted.
func applyTypeDefaults(u *config.Upstream) {
	if u.Port == 0 {
		if u.Type == config.TypeHTTP {
			u.Port = 443
		} else {
			u.Port = 5432
		}
	}
	if u.Env == "" {
		if u.Type == config.TypeHTTP {
			u.Env = defaultEnvName(u.Name, "KEY")
		} else {
			u.Env = defaultEnvName(u.Name, "URL")
		}
	}
	if u.Type == config.TypeHTTP && u.EnvURL == "" {
		u.EnvURL = defaultEnvName(u.Name, "URL")
	}
}

func promptLabel(u config.Upstream) string {
	if u.User != "" {
		return fmt.Sprintf("Password for %s@%s", u.User, u.Host)
	}
	return fmt.Sprintf("Secret for %s", u.Host)
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

func defaultEnvName(name, suffix string) string {
	s := strings.ToUpper(name)
	s = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
	return "CLOAK_" + s + "_" + suffix
}

func readPassword(label string) (string, error) {
	if addFlags.passwordStdin {
		data, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("no terminal for credential prompt (use --password-stdin when scripting): %w", err)
	}
	defer tty.Close()
	fmt.Fprintf(os.Stderr, "%s: ", label)
	pw, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}
