package authn_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkauthz"
)

// A second issuer may hand out any group name it likes; a binding written
// for the first issuer's group must not be satisfied by it.
func TestAGroupFromTheWrongIssuerSatisfiesNoBinding(t *testing.T) {
	a, b, is := authn.TwoIssuers(t)
	ctx := context.Background()
	p := authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")
	src := `
[roles.admin]
permissions = ["*:*"]

[[bind]]
role = "admin"
groups = ["figaro-admin@auth.kelliher.info"]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"sub": "u-9", "preferred_username": "mallory", "groups": []string{"figaro-admin"}}
	for name, c := range map[string]struct {
		iss  *authn.TestIssuer
		want bool
	}{"the bound issuer": {a, true}, "another issuer": {b, false}} {
		s, err := is.Open(ctx, c.iss.Mint("u-9", "figaro-cli", time.Hour, user))
		if err != nil {
			t.Fatal(err)
		}
		sub, err := s.Subject(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Allow(sub, rpc.VerbUse, authz.Daemon).Allowed; got != c.want {
			t.Errorf("%s: allowed %v", name, got)
		}
	}
}
