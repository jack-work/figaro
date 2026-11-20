package authz

import (
	"testing"

	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/jkauthz"
)

// The provisioner's grant is computed for one lease inside the daemon. A name
// that only resembles it must never be taken for it.
func TestOnlyTheExactNameIsTheProvisioner(t *testing.T) {
	if got := authn.ProvisionerPrincipal().String(); got != "service:provisioner@local:pool" {
		t.Fatalf("the provisioner is %q", got)
	}
	if !authn.IsProvisioner(authn.ProvisionerPrincipal()) {
		t.Fatal("the provisioner is not recognised")
	}
	for _, p := range []jkauthz.Principal{
		{Kind: jkauthz.KindUser, ID: "provisioner@local:pool"},
		{Kind: jkauthz.KindSandbox, ID: "provisioner@local:pool"},
		{Kind: authn.KindService, ID: "provisioner@local:pool", Node: "spain"},
		{Kind: authn.KindService, ID: "provisioner@local:pool@auth.kelliher.info"},
		{Kind: authn.KindService, ID: "provisioner@local:box"},
		{Kind: authn.KindService, ID: "provisioner"},
		{Kind: authn.KindService, ID: "broker-server@local:pool"},
		{Kind: authn.KindService, ID: jkauthz.Any},
	} {
		if authn.IsProvisioner(p) {
			t.Errorf("%s is taken for the provisioner", p)
		}
	}
}

// An issuer's scope is its host. If a URL could yield local:pool or
// local:box, a token from that issuer would carry a daemon-made name.
func TestNoIssuerURLYieldsADaemonMadeScope(t *testing.T) {
	for _, url := range []string{"https://local:pool", "https://local:box", "https://local:pool/", "local:pool", "https://x@local:pool"} {
		if scope, err := authn.IssuerScope(url); err == nil && (scope == authn.PoolScope || scope == authn.BoxScope) {
			t.Errorf("issuer %q has scope %q", url, scope)
		}
	}
}
