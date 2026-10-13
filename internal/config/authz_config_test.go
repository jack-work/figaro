package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The [authz] block has to survive an actual TOML round trip through Load: a
// typo in a tag makes the knob unreachable while every unit test stays green.
func TestLoadParsesAuthzSection(t *testing.T) {
	dir := t.TempDir()
	body := "default_outfit = \"opus5\"\n\n[authz]\npolicy = \"default\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := l.AuthzPolicy(); got != "default" {
		t.Fatalf("policy = %q, want default", got)
	}
}

func TestLoadDefaultsAuthzOff(t *testing.T) {
	l, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := l.AuthzPolicy(); got != "allow-all" {
		t.Fatalf("policy = %q, want allow-all", got)
	}
}

func TestAuthzPolicyNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"":            "allow-all",
		"  ":          "allow-all",
		"allow-all":   "allow-all",
		"none":        "allow-all",
		"custom":      "allow-all",
		"DEFAULT":     "default",
		"  Default  ": "default",
	} {
		l := &Loaded{Config: Config{Authz: AuthzConfig{Policy: in}}}
		if got := l.AuthzPolicy(); got != want {
			t.Fatalf("AuthzPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}
