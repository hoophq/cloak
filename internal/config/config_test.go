package config

import (
	"path/filepath"
	"testing"
)

func sample() Upstream {
	return Upstream{
		Name: "pg-prod", Type: TypePostgres, Host: "db.internal", Port: 5432,
		Database: "app", User: "app_user", ListenPort: 5433,
		Env: "DATABASE_URL", TLS: TLSVerifyFull,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	c := &Config{Upstreams: []Upstream{sample()}}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Upstreams) != 1 || got.Upstreams[0] != sample() {
		t.Fatalf("round trip = %+v", got.Upstreams)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams) != 0 {
		t.Fatalf("want empty config, got %+v", c)
	}
}

func TestNextListenPort(t *testing.T) {
	c := &Config{}
	if got := c.NextListenPort(); got != FirstListenPort {
		t.Fatalf("empty config port = %d", got)
	}
	u := sample()
	u.ListenPort = 5440
	c.Upstreams = append(c.Upstreams, u)
	if got := c.NextListenPort(); got != 5441 {
		t.Fatalf("port after 5440 = %d", got)
	}
}

func TestRemove(t *testing.T) {
	c := &Config{Upstreams: []Upstream{sample()}}
	if !c.Remove("pg-prod") {
		t.Fatal("expected removal")
	}
	if c.Remove("pg-prod") {
		t.Fatal("expected second removal to report missing")
	}
}

func TestValidate(t *testing.T) {
	good := sample()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Upstream){
		"empty name":   func(u *Upstream) { u.Name = "" },
		"bad type":     func(u *Upstream) { u.Type = "mysql" },
		"no host":      func(u *Upstream) { u.Host = "" },
		"bad port":     func(u *Upstream) { u.Port = 0 },
		"bad listen":   func(u *Upstream) { u.ListenPort = 0 },
		"no env":       func(u *Upstream) { u.Env = "" },
		"no user":      func(u *Upstream) { u.User = "" },
		"bad tls":      func(u *Upstream) { u.TLS = "prefer" },
		"http no auth": func(u *Upstream) { u.Type = TypeHTTP; u.User = ""; u.Auth = "" },
		"http bad auth": func(u *Upstream) {
			u.Type = TypeHTTP
			u.User = ""
			u.Auth = "cookie:sid"
		},
	} {
		u := sample()
		mutate(&u)
		if err := u.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestValidateHTTP(t *testing.T) {
	base := Upstream{
		Name: "openai", Type: TypeHTTP, Host: "api.openai.com", Port: 443,
		ListenPort: 5434, Env: "OPENAI_API_KEY", EnvURL: "OPENAI_BASE_URL",
		TLS: TLSVerifyFull,
	}
	for _, auth := range []string{"bearer", "header:x-api-key"} {
		u := base
		u.Auth = auth
		if err := u.Validate(); err != nil {
			t.Errorf("auth %q: unexpected error %v", auth, err)
		}
	}
}

func TestValidateSocket(t *testing.T) {
	pg := sample()
	pg.Socket = true
	if err := pg.Validate(); err != nil {
		t.Errorf("postgres socket should validate: %v", err)
	}

	h := sample()
	h.Type = TypeHTTP
	h.Auth = "bearer"
	h.Socket = true
	if err := h.Validate(); err == nil {
		t.Error("socket mode on an http upstream should be rejected")
	}
}

func TestParseAuth(t *testing.T) {
	cases := map[string]struct {
		header, prefix string
		wantErr        bool
	}{
		"bearer":           {"Authorization", "Bearer ", false},
		"header:x-api-key": {"x-api-key", "", false},
		"header:":          {"", "", true},
		"basic":            {"", "", true},
		"header:bad name":  {"", "", true},
	}
	for auth, want := range cases {
		h, p, err := ParseAuth(auth)
		if (err != nil) != want.wantErr {
			t.Errorf("ParseAuth(%q) err = %v, wantErr %v", auth, err, want.wantErr)
			continue
		}
		if err == nil && (h != want.header || p != want.prefix) {
			t.Errorf("ParseAuth(%q) = (%q, %q), want (%q, %q)", auth, h, p, want.header, want.prefix)
		}
	}
}
