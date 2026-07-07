package cmd

import (
	"strings"
	"testing"
)

func withCleanAddFlags(t *testing.T) {
	t.Helper()
	saved := addFlags
	addFlags.port = 5432 // flag default
	t.Cleanup(func() { addFlags = saved })
}

func TestApplyURL(t *testing.T) {
	withCleanAddFlags(t)
	if err := applyURL("postgres://app_user@db.internal:6432/app"); err != nil {
		t.Fatal(err)
	}
	if addFlags.host != "db.internal" || addFlags.port != 6432 ||
		addFlags.user != "app_user" || addFlags.db != "app" {
		t.Fatalf("parsed flags = %+v", addFlags)
	}
}

func TestApplyURLDefaults(t *testing.T) {
	withCleanAddFlags(t)
	if err := applyURL("postgresql://u@h"); err != nil {
		t.Fatal(err)
	}
	if addFlags.port != 5432 {
		t.Fatalf("port without URL port = %d, want default 5432", addFlags.port)
	}
	if addFlags.db != "" {
		t.Fatalf("db without URL path = %q, want empty", addFlags.db)
	}
}

func TestApplyURLRejectsPassword(t *testing.T) {
	withCleanAddFlags(t)
	err := applyURL("postgres://u:hunter2@h:5432/db")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password rejection, got %v", err)
	}
}

func TestApplyURLRejectsOtherSchemes(t *testing.T) {
	withCleanAddFlags(t)
	if err := applyURL("mysql://u@h/db"); err == nil {
		t.Fatal("expected non-postgres scheme to be rejected")
	}
}

func TestDefaultEnvName(t *testing.T) {
	for in, want := range map[string]string{
		"pg-prod":   "CLOAK_PG_PROD_URL",
		"db.main":   "CLOAK_DB_MAIN_URL",
		"analytics": "CLOAK_ANALYTICS_URL",
	} {
		if got := defaultEnvName(in); got != want {
			t.Errorf("defaultEnvName(%q) = %q, want %q", in, got, want)
		}
	}
}
