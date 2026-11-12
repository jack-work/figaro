package authz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
)

func poolPolicy(t *testing.T, principals, groups string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	src := `
[roles.admin]
permissions = ["*:*"]
[roles.reader]
permissions = ["read:*"]

[[bind]]
role = "admin"
principals = ["user:jack@auth.kelliher.info"]

[[bind]]
role = "reader"
principals = [` + principals + `]
groups = [` + groups + `]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func policiesFor(pool bool) *Policies {
	p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
	p.SetPoolDaemon(pool)
	p.SetIssuerScopes([]string{"auth.kelliher.info"})
	return p
}

// A pool daemon's policy names broker's client and the admin and nobody else.
// Each bind here loads on an ordinary daemon, so what refuses it is the pool.
func TestAPoolDaemonsPolicyBindsOnlyNamedLocalPrincipals(t *testing.T) {
	for name, c := range map[string]struct{ principals, groups string }{
		"every user":               {`"user:*"`, ``},
		"every service":            {`"service:*"`, ``},
		"every box":                {`"sandbox:*"`, ``},
		"one box":                  {`"sandbox:slot3-g7@local:box"`, ``},
		"a principal of a peer":    {`"gluck/user:1000"`, ``},
		"a node":                   {`"node:gluck"`, ``},
		"a troupe's service":       {`"service:ci@troupe:kelliher"`, ``},
		"an api key's user":        {`"user:ci@troupe:kelliher"`, ``},
		"a troupe's group":         {`"service:broker-server@auth.kelliher.info"`, `"admins@troupe:kelliher"`},
		"another issuer's group":   {`"service:broker-server@auth.kelliher.info"`, `"rou2-admin@login.example"`},
		"a group of no issuer":     {`"service:broker-server@auth.kelliher.info"`, `"rou2-admin"`},
		"a group of a longer host": {`"service:broker-server@auth.kelliher.info"`, `"rou2-admin@auth.kelliher.info.example"`},
		"a wildcard beside a name": {`"service:broker-server@auth.kelliher.info", "service:*"`, ``},
	} {
		path := poolPolicy(t, c.principals, c.groups)
		if err := policiesFor(false).Reload(path); err != nil {
			t.Errorf("%s: an ordinary daemon refused it, so the pool case proves nothing: %v", name, err)
		}
		err := policiesFor(true).Reload(path)
		if err == nil {
			t.Errorf("%s: a pool daemon loaded it", name)
		} else if !strings.Contains(err.Error(), "a daemon that leases slots binds") {
			t.Errorf("%s: refused for another reason: %v", name, err)
		}
	}
	good := poolPolicy(t, `"service:broker-server@auth.kelliher.info"`, `"figaro-admin@auth.kelliher.info"`)
	if err := policiesFor(true).Reload(good); err != nil {
		t.Fatalf("broker's client and an issuer's group, by name: %v", err)
	}
}

// With no file named an ordinary daemon takes the default policy, which binds
// the owner and every box. A pool daemon must be told its policy.
func TestAPoolDaemonTakesNoDefaultPolicy(t *testing.T) {
	if err := policiesFor(false).Reload(""); err != nil {
		t.Fatalf("an ordinary daemon with no policy file: %v", err)
	}
	p := policiesFor(true)
	if err := p.Reload(""); err == nil {
		t.Fatal("a pool daemon took the default policy")
	}
	if p.HoldsRole(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}}) {
		t.Fatal("the refused default policy is in force all the same")
	}
}

func TestARefusedPoolPolicyLeavesThePreviousOneInForce(t *testing.T) {
	admin := jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "jack@auth.kelliher.info"}}
	everyone := jkauthz.Subject{Principal: jkauthz.Principal{Kind: KindService, ID: "anyone@auth.kelliher.info"}}
	p := policiesFor(true)
	if err := p.Reload(poolPolicy(t, `"service:broker-server@auth.kelliher.info"`, ``)); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(poolPolicy(t, `"service:*"`, ``)); err == nil {
		t.Fatal("the wildcard loaded")
	}
	if !p.HoldsRole(admin) || p.HoldsRole(everyone) {
		t.Fatalf("after the refusal: admin holds a role %v, any service holds one %v", p.HoldsRole(admin), p.HoldsRole(everyone))
	}
}
