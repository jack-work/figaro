package authz

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

const mixedPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = ["user:1000", "user:jack@auth.kelliher.info", "node:spain", "user:jack", "sandbox:dev-3", "spain/user:gluck"]
groups = ["figaro-admin@auth.kelliher.info", "troupe-members"]
`

// A policy written before principals carried their issuer names people no
// token produces any more; it loads, but the operator is told what to write.
func TestUnscopedTokenNamesAreFound(t *testing.T) {
	got := UnscopedNames([]byte(mixedPolicy))
	if want := []string{"user:jack", "group troupe-members"}; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestTheScopedSpellingNamesTheConfiguredIssuers(t *testing.T) {
	for _, c := range []struct {
		name   string
		scopes []string
		want   string
	}{
		{"user:jack", []string{"auth.kelliher.info"}, "user:jack@auth.kelliher.info"},
		{"group troupe-members", []string{"auth.kelliher.info"}, "group troupe-members@auth.kelliher.info"},
		{"user:jack", []string{"a.example", "b.example/realms/x"}, "user:jack@{a.example,b.example/realms/x}"},
		{"user:jack", nil, "user:jack@<issuer host>"},
	} {
		if got := ScopedSpelling(c.name, c.scopes); got != c.want {
			t.Errorf("%s %v: %s", c.name, c.scopes, got)
		}
	}
}

func TestIssuerScopesComeFromTheIssuersAndTheEdge(t *testing.T) {
	got := IssuerScopes(config.AuthzConfig{
		Upstream: config.UpstreamConfig{Issuer: "https://auth.kelliher.info"},
		Issuers:  []config.IssuerConfig{{Issuer: "https://auth.kelliher.info/"}, {Issuer: "https://id.example.org/realms/guests"}},
	})
	if want := []string{"auth.kelliher.info", "id.example.org/realms/guests"}; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestReloadWarnsOnUnscopedNamesAndStillLoads(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(mixedPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	p.SetIssuerScopes([]string{"auth.kelliher.info"})
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"write=user:jack@auth.kelliher.info", `write="group troupe-members@auth.kelliher.info"`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in\n%s", want, out)
		}
	}
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Errorf("%d warnings:\n%s", n, out)
	}
	if !p.Allow(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "jack"}}, "read", Daemon).Allowed {
		t.Error("an unscoped grant is warned about, not dropped")
	}
}

const wildcardPolicy = `
[roles.r]
grants = [{ verbs = ["read"], on = ["*"] }]

[[bind]]
role = "r"
principals = ["user:*", "service:*", "sandbox:*", "node:*", "spain/user:*", "user:jack@auth.kelliher.info"]
`

// user:*, service:* and sandbox:* match that kind from every issuer, troupe
// issuer and api key; node:* only peers and local issuer clients. A node's
// own policy may still say so, but it is told.
func TestReloadWarnsOnWildcardsAcrossIssuersAndStillLoads(t *testing.T) {
	if got, want := WildcardsAcrossIssuers([]byte(wildcardPolicy)), []string{"user:*", "service:*", "sandbox:*"}; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(wildcardPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 3 {
		t.Errorf("%d warnings:\n%s", n, out)
	}
	for _, name := range []string{"user:*", "service:*", "sandbox:*"} {
		if !strings.Contains(out, "binds "+name+",") {
			t.Errorf("no warning for %s in\n%s", name, out)
		}
	}
	if !p.Allow(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "ana@id.example.org"}}, "read", Daemon).Allowed {
		t.Error("a wildcard in the node's own policy is warned about, not dropped")
	}
}
