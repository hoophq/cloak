// Package envimport scans .env files for credential-shaped values, imports
// the ones cloak can proxy, and rewrites the file so nothing sensitive is
// left for an agent to read.
package envimport

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoophq/cloak/internal/config"
)

// ManagedComment is written above each rewritten entry.
const ManagedComment = "# managed by cloak — real credential in the OS keychain; run `cloak start` so this loopback DSN resolves"

// Candidate is an importable entry: a postgres DSN with an embedded password.
type Candidate struct {
	LineNo   int // index into the file's lines
	Key      string
	Export   bool
	Upstream config.Upstream // Name, ListenPort and Env are assigned by the caller
	Password string
}

// Warning flags a credential-shaped value cloak cannot import.
type Warning struct {
	LineNo int
	Key    string
	Reason string
}

// Scan classifies every entry of a .env file's lines.
func Scan(lines []string) ([]Candidate, []Warning) {
	var cands []Candidate
	var warns []Warning
	for i, raw := range lines {
		key, value, export, ok := parseLine(raw)
		if !ok || value == "" {
			continue
		}
		if strings.HasPrefix(value, "postgres://") || strings.HasPrefix(value, "postgresql://") {
			c, w := classifyPostgres(i, key, export, value)
			if c != nil {
				cands = append(cands, *c)
			}
			if w != nil {
				warns = append(warns, *w)
			}
		} else if looksLikeCredential(key, value) {
			warns = append(warns, Warning{i, key, "credential-shaped value; cloak cannot proxy this yet"})
		}
	}
	return cands, warns
}

func classifyPostgres(lineNo int, key string, export bool, value string) (*Candidate, *Warning) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, &Warning{lineNo, key, "postgres DSN failed to parse: " + err.Error()}
	}
	if u.User == nil {
		return nil, &Warning{lineNo, key, "postgres DSN has no embedded credential; nothing to move"}
	}
	if u.User.Username() == "cloak" {
		return nil, nil // already a cloak DSN or placeholder
	}
	pw, has := u.User.Password()
	if !has || pw == "" {
		return nil, &Warning{lineNo, key, "postgres DSN has no embedded password; nothing to move"}
	}
	if u.User.Username() == "" {
		return nil, &Warning{lineNo, key, "postgres DSN has a password but no username"}
	}
	host := u.Hostname()
	if host == "" {
		host = "localhost"
	}
	port := 5432
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, &Warning{lineNo, key, "postgres DSN has an invalid port"}
		}
		port = n
	}
	tlsMode := config.TLSVerifyFull
	if u.Query().Get("sslmode") == "disable" {
		tlsMode = config.TLSDisable
	}
	return &Candidate{
		LineNo: lineNo, Key: key, Export: export, Password: pw,
		Upstream: config.Upstream{
			Type: config.TypePostgres, Host: host, Port: port,
			Database: strings.TrimPrefix(u.Path, "/"),
			User:     u.User.Username(), TLS: tlsMode,
		},
	}, nil
}

func parseLine(raw string) (key, value string, export, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "#") {
		return
	}
	if rest, found := strings.CutPrefix(s, "export "); found {
		export = true
		s = strings.TrimSpace(rest)
	}
	k, v, found := strings.Cut(s, "=")
	if !found {
		return
	}
	k = strings.TrimSpace(k)
	if k == "" || strings.ContainsAny(k, " \t") {
		return
	}
	return k, unquote(strings.TrimSpace(v)), export, true
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

var secretKeyHints = []string{
	"SECRET", "TOKEN", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "PRIVATE_KEY", "CREDENTIAL",
}

var secretValuePrefixes = []string{
	"sk-", "sk_live_", "rk_live_", "ghp_", "gho_", "github_pat_", "AKIA", "xoxb-", "xoxp-", "glpat-",
}

func looksLikeCredential(key, value string) bool {
	if len(value) < 8 {
		return false
	}
	for _, p := range secretValuePrefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	// Any URL with an embedded password (mysql://, redis://, amqp://, ...).
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.User != nil {
		if _, has := u.User.Password(); has {
			return true
		}
	}
	upper := strings.ToUpper(key)
	for _, h := range secretKeyHints {
		if strings.Contains(upper, h) {
			return true
		}
	}
	return false
}

// Rewrite replaces each candidate's line with a managed comment plus the
// placeholder value, leaving every other line untouched.
func Rewrite(lines []string, cands []Candidate, placeholder func(Candidate) string) []string {
	byLine := make(map[int]Candidate, len(cands))
	for _, c := range cands {
		byLine[c.LineNo] = c
	}
	out := make([]string, 0, len(lines)+len(cands))
	for i, raw := range lines {
		c, ok := byLine[i]
		if !ok {
			out = append(out, raw)
			continue
		}
		if len(out) == 0 || out[len(out)-1] != ManagedComment {
			out = append(out, ManagedComment)
		}
		line := c.Key + "=" + placeholder(c)
		if c.Export {
			line = "export " + line
		}
		out = append(out, line)
	}
	return out
}

// DeriveName turns an env key into an upstream name (DATABASE_URL →
// database-url), suffixing -2, -3, ... while taken reports a collision.
func DeriveName(key string, taken func(string) bool) string {
	var b strings.Builder
	lastDash := true // trim leading dashes
	for _, r := range strings.ToLower(key) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.TrimSuffix(b.String(), "-")
	if base == "" {
		base = "imported"
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func backupDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "cloak", "backups"), nil
}

// backupPrefix names backups so they sort by creation time per source file.
func backupPrefix(path string) (dir, prefix string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	dir, err = backupDir()
	if err != nil {
		return "", "", err
	}
	return dir, url.PathEscape(abs) + ".", nil
}

// Backup copies the file into cloak's state dir (outside any project tree)
// and returns the backup path.
func Backup(path string) (string, error) {
	dir, prefix, err := backupPrefix(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	bp := filepath.Join(dir, fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()))
	if err := os.WriteFile(bp, data, 0o600); err != nil {
		return "", err
	}
	return bp, nil
}

// LatestBackup returns the most recent backup taken for path.
func LatestBackup(path string) (string, error) {
	dir, prefix, err := backupPrefix(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no backups found for %s", path)
	}
	var best string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no backups found for %s", path)
	}
	return filepath.Join(dir, best), nil
}
