package daemon

import (
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

// A web door the daemon cannot verify callers on must not open: each of
// these fails the daemon's start rather than serving an upgrade.
func TestAWebDoorWithoutTheConfigToVerifyCallersDoesNotStart(t *testing.T) {
	const listen, cli = "/run/figaro/web.sock", "/run/figaro/angelus.sock"
	for name, c := range map[string]struct {
		authn string
		cfg   config.AuthzConfig
	}{
		"no authn":                         {"", config.AuthzConfig{}},
		"an unknown authn":                 {"none", config.AuthzConfig{}},
		"bearer with no issuers":           {"bearer", config.AuthzConfig{}},
		"upstream with no [authz] at all":  {"upstream", config.AuthzConfig{}},
		"upstream reverifying, no issuers": {"upstream", config.AuthzConfig{Upstream: config.UpstreamConfig{Issuer: "https://auth.kelliher.info", Reverify: true}}},
	} {
		if admit, err := webAuthenticator(c.authn, listen, cli, c.cfg, nil); err == nil || admit != nil {
			t.Errorf("%s: started (%v)", name, err)
		}
	}
	if _, err := webAuthenticator("upstream", listen, cli, config.AuthzConfig{Upstream: config.UpstreamConfig{Issuer: "https://auth.kelliher.info"}}, nil); err != nil {
		t.Fatalf("upstream naming its issuer: %v", err)
	}
}
