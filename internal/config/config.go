// Package config holds the on-disk registry of upstreams. It never contains
// credentials: real secrets live in the OS keychain, referenced by upstream
// name.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	TypePostgres = "postgres"
	TypeHTTP     = "http"

	TLSVerifyFull = "verify-full"
	TLSDisable    = "disable"

	// AuthBearer injects the credential as "Authorization: Bearer <secret>".
	AuthBearer = "bearer"
	// AuthHeaderPrefix selects a raw header, e.g. "header:x-api-key".
	AuthHeaderPrefix = "header:"

	// FirstListenPort is where automatic local port allocation starts.
	FirstListenPort = 5433
)

// Upstream describes one real service Cloak fronts with a local listener.
type Upstream struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	Database   string `yaml:"database,omitempty"` // postgres only
	User       string `yaml:"user,omitempty"`     // postgres only
	Socket     bool   `yaml:"socket,omitempty"`   // postgres only: unix socket instead of loopback TCP
	Auth       string `yaml:"auth,omitempty"`     // http only: bearer | header:<name>
	ListenPort int    `yaml:"listen_port"`
	Env        string `yaml:"env,omitempty"`
	EnvURL     string `yaml:"env_url,omitempty"` // http only: var for the local base URL
	TLS        string `yaml:"tls"`
}

func (u *Upstream) Addr() string {
	return fmt.Sprintf("%s:%d", u.Host, u.Port)
}

// DBName is the database connections land on: the configured database,
// defaulting to the username (the libpq convention).
func (u *Upstream) DBName() string {
	if u.Database != "" {
		return u.Database
	}
	return u.User
}

func (u *Upstream) Validate() error {
	if u.Name == "" {
		return fmt.Errorf("upstream name is required")
	}
	if u.Host == "" {
		return fmt.Errorf("upstream %q: host is required", u.Name)
	}
	if u.Port <= 0 || u.Port > 65535 {
		return fmt.Errorf("upstream %q: invalid port %d", u.Name, u.Port)
	}
	if u.ListenPort <= 0 || u.ListenPort > 65535 {
		return fmt.Errorf("upstream %q: invalid listen port %d", u.Name, u.ListenPort)
	}
	if u.TLS != TLSVerifyFull && u.TLS != TLSDisable {
		return fmt.Errorf("upstream %q: tls must be %q or %q", u.Name, TLSVerifyFull, TLSDisable)
	}
	if u.Env == "" {
		return fmt.Errorf("upstream %q: env is required (the var the fake credential is injected as)", u.Name)
	}
	switch u.Type {
	case TypePostgres:
		if u.User == "" {
			return fmt.Errorf("upstream %q: user is required", u.Name)
		}
	case TypeHTTP:
		if _, _, err := ParseAuth(u.Auth); err != nil {
			return fmt.Errorf("upstream %q: %w", u.Name, err)
		}
	default:
		return fmt.Errorf("upstream %q: unsupported type %q (%q or %q)", u.Name, u.Type, TypePostgres, TypeHTTP)
	}
	if u.Socket && u.Type != TypePostgres {
		return fmt.Errorf("upstream %q: socket mode is postgres-only", u.Name)
	}
	return nil
}

// ParseAuth resolves an http auth placement into the header to set and the
// value prefix the credential is wrapped with.
func ParseAuth(auth string) (header, prefix string, err error) {
	if auth == AuthBearer {
		return "Authorization", "Bearer ", nil
	}
	if name, ok := strings.CutPrefix(auth, AuthHeaderPrefix); ok && name != "" && !strings.ContainsAny(name, " \t:") {
		return name, "", nil
	}
	return "", "", fmt.Errorf("auth must be %q or %q<name>, got %q", AuthBearer, AuthHeaderPrefix, auth)
}

type Config struct {
	Upstreams []Upstream `yaml:"upstreams"`
}

// Path returns the default config location: $XDG_CONFIG_HOME/cloak/config.yaml,
// falling back to ~/.config/cloak/config.yaml.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cloak", "config.yaml"), nil
}

// Load reads the config at path. A missing file yields an empty config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Config) Find(name string) (*Upstream, bool) {
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i], true
		}
	}
	return nil, false
}

// Remove deletes the named upstream, reporting whether it existed.
func (c *Config) Remove(name string) bool {
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			c.Upstreams = append(c.Upstreams[:i], c.Upstreams[i+1:]...)
			return true
		}
	}
	return false
}

// ListenPortOwner returns the name of the upstream bound to the given local
// port, if any — used to reject a colliding --listen-port before it reaches
// the daemon, where a double bind would fail.
func (c *Config) ListenPortOwner(port int) (string, bool) {
	for i := range c.Upstreams {
		if c.Upstreams[i].ListenPort == port {
			return c.Upstreams[i].Name, true
		}
	}
	return "", false
}

// NextListenPort picks the next free local port slot, keeping assignments
// stable across sessions (they are persisted in the config).
func (c *Config) NextListenPort() int {
	next := FirstListenPort
	for _, u := range c.Upstreams {
		if u.ListenPort >= next {
			next = u.ListenPort + 1
		}
	}
	return next
}
