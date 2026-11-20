package authz

import (
	"testing"

	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

func TestOwnerOfIsTheDaemonUIDUntilConfigNamesAPerson(t *testing.T) {
	self := jkauthz.Principal{Kind: jkauthz.KindUser, ID: "2001"}
	got, err := authn.OwnerOf(config.AuthzConfig{}, self)
	if err != nil || got != self {
		t.Fatalf("unset owner: %v, %v; want the daemon's own principal", got, err)
	}
	got, err = authn.OwnerOf(config.AuthzConfig{Owner: "user:1000"}, self)
	if err != nil || got != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}) {
		t.Fatalf("named owner: %v, %v", got, err)
	}
	for _, bad := range []string{"1000", "spain/user:1000", "troupe:kelliher", "user:"} {
		if p, err := authn.OwnerOf(config.AuthzConfig{Owner: bad}, self); err == nil {
			t.Errorf("owner %q accepted as %v", bad, p)
		}
	}
}

func TestTheDefaultPolicyBindsTheNamedOwnerAndNotTheDaemon(t *testing.T) {
	owner := jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}
	p := NewPolicies(owner, nil)
	if err := p.Reload(""); err != nil {
		t.Fatal(err)
	}
	if !p.HoldsRole(jkauthz.Subject{Principal: owner}) {
		t.Fatal("the named owner holds no role under the default policy")
	}
	if p.HoldsRole(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "2001"}}) {
		t.Fatal("the daemon's uid holds a role under a default policy that names a person")
	}
}
