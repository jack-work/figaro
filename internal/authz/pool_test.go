package authz

import (
	"testing"

	"github.com/jack-work/jkauthz"
)

// The provisioner's grant is computed for one lease inside the daemon. A name
// that only resembles it must never be taken for it.
func TestOnlyTheExactNameIsTheProvisioner(t *testing.T) {
	if got := ProvisionerPrincipal().String(); got != "service:provisioner@local:pool" {
		t.Fatalf("the provisioner is %q", got)
	}
	if !IsProvisioner(ProvisionerPrincipal()) {
		t.Fatal("the provisioner is not recognised")
	}
	for _, p := range []jkauthz.Principal{
		{Kind: jkauthz.KindUser, ID: "provisioner@local:pool"},
		{Kind: jkauthz.KindSandbox, ID: "provisioner@local:pool"},
		{Kind: KindService, ID: "provisioner@local:pool", Node: "spain"},
		{Kind: KindService, ID: "provisioner@local:pool@auth.kelliher.info"},
		{Kind: KindService, ID: "provisioner@local:box"},
		{Kind: KindService, ID: "provisioner"},
		{Kind: KindService, ID: "broker-server@local:pool"},
		{Kind: KindService, ID: jkauthz.Any},
	} {
		if IsProvisioner(p) {
			t.Errorf("%s is taken for the provisioner", p)
		}
	}
}

// An issuer's scope is its host. If a URL could yield local:pool or
// local:box, a token from that issuer would carry a daemon-made name.
func TestNoIssuerURLYieldsADaemonMadeScope(t *testing.T) {
	for _, url := range []string{"https://local:pool", "https://local:box", "https://local:pool/", "local:pool", "https://x@local:pool"} {
		if scope, err := IssuerScope(url); err == nil && (scope == PoolScope || scope == BoxScope) {
			t.Errorf("issuer %q has scope %q", url, scope)
		}
	}
}
