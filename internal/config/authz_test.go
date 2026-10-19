package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthzTableLoadsAndThePolicyPathResolvesAgainstTheConfigDir(t *testing.T) {
	dir := t.TempDir()
	src := `
[authz]
policy = "policy.toml"

[authz.peercred]
uids = { "1001" = "sandbox:dev-3" }

[authz.upstream]
issuer = "https://auth.kelliher.info"

[[authz.issuers]]
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
	if a.Peercred.UIDs["1001"] != "sandbox:dev-3" || len(a.Issuers) != 1 || a.Issuers[0].Clients["sandbox-dev-3"] != "sandbox" || a.Issuers[0].Users[0] != "figaro-cli" || a.Upstream.Issuer != "https://auth.kelliher.info" {
		t.Fatalf("%+v", a)
	}
}

// The single-issuer table carried unscoped principals; loading it quietly
// would keep a policy that no longer matches anyone, so it is refused by name.
func TestTheRetiredBearerTableIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	src := "[authz.bearer]\nissuer = \"https://auth.kelliher.info\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "[[authz.issuers]]") {
		t.Fatalf("%v", err)
	}
}

func TestNoPolicyConfiguredMeansNoPath(t *testing.T) {
	if p := (&Loaded{ConfigDir: t.TempDir()}).PolicyPath(); p != "" {
		t.Fatalf("%q", p)
	}
}
