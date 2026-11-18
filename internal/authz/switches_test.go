package authz

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

// Defends the rollout default: the three P5 switches ship off, and the
// daemon config's [authz] table is what turns each on.
func TestTheSwitchesShipOffAndFollowTheConfig(t *testing.T) {
	for body, want := range map[string]Enforcement{
		"":                                     {},
		"[authz]\nenforce_key_grants = true\n": {KeyGrants: true},
		"[authz]\nenforce_child_policy = true\nenforce_lineage_reach = true\n": {ChildPolicy: true, LineageReach: true},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := EnforcementOf(loaded.Config.Authz); got != want {
			t.Errorf("config %q: %+v, want %+v", body, got, want)
		}
		p := NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, nil)
		p.SetEnforcement(EnforcementOf(loaded.Config.Authz))
		for s, on := range map[Switch]bool{KeyGrants: want.KeyGrants, ChildPolicy: want.ChildPolicy, LineageReach: want.LineageReach} {
			if p.Enforces(s) != on {
				t.Errorf("config %q: %s enforces %v, want %v", body, s, p.Enforces(s), on)
			}
		}
	}
}
