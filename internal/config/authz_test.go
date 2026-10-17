package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthzTableLoadsAndThePolicyPathResolvesAgainstTheConfigDir(t *testing.T) {
	dir := t.TempDir()
	src := `
[authz]
policy = "policy.toml"

[authz.peercred]
uids = { "1001" = "sandbox:dev-3" }

[authz.bearer]
issuer = "https://auth.kelliher.info"
jwks = "http://127.0.0.1:9091/jwks.json"
userinfo = "http://127.0.0.1:9091/api/oidc/userinfo"
users = ["figaro-cli"]
clients = { "sandbox-dev-3" = "sandbox" }
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.PolicyPath(); got != filepath.Join(dir, "policy.toml") {
		t.Fatalf("policy path %q", got)
	}
	a := l.Config.Authz
	if a.Peercred.UIDs["1001"] != "sandbox:dev-3" || a.Bearer == nil || a.Bearer.Clients["sandbox-dev-3"] != "sandbox" || a.Bearer.Users[0] != "figaro-cli" {
		t.Fatalf("%+v", a)
	}
}

func TestNoPolicyConfiguredMeansNoPath(t *testing.T) {
	if p := (&Loaded{ConfigDir: t.TempDir()}).PolicyPath(); p != "" {
		t.Fatalf("%q", p)
	}
}
