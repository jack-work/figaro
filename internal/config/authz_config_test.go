package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The [authz] block has to survive an actual TOML round trip through Load.
// Every other test in this change constructs config.Loaded in Go, which proves
// nothing about the toml tags, a typo there makes the whole feature
// unreachable in production while every unit test stays green.
func TestLoadParsesAuthzSection(t *testing.T) {
	dir := t.TempDir()
	body := `
default_outfit = "opus5"

[authz]
caller_identity = true
policy = "default"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !l.CallerIdentityEnabled() {
		t.Fatal("caller_identity = true did not parse")
	}
	if got := l.AuthzPolicy(); got != "default" {
		t.Fatalf("policy = %q, want default", got)
	}
}

// An explicit false must be distinguishable from unset, which is why the field
// is a pointer. If it were a bool, `caller_identity = false` and an absent key
// would be the same value and the switch could never be turned back off in a
// config that inherits a true from elsewhere.
func TestLoadDistinguishesExplicitFalseFromUnset(t *testing.T) {
	dir := t.TempDir()
	body := "[authz]\ncaller_identity = false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.Config.Authz.CallerIdentity == nil {
		t.Fatal("explicit false parsed as unset")
	}
	if l.CallerIdentityEnabled() {
		t.Fatal("explicit false read as enabled")
	}
}

// No [authz] section at all is the default install, and it must be neutral.
func TestLoadDefaultsAuthzOff(t *testing.T) {
	l, err := Load(t.TempDir()) // no config.toml at all
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.CallerIdentityEnabled() {
		t.Fatal("authn enabled with no config")
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
		"off":         "allow-all",
		"DEFAULT":     "default",
		"  Default  ": "default",
		"custom":      "custom",
	} {
		l := &Loaded{Config: Config{Authz: AuthzConfig{Policy: in}}}
		if got := l.AuthzPolicy(); got != want {
			t.Fatalf("AuthzPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}
