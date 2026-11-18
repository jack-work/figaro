package angelus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
)

type switchRow struct {
	sw     authz.Switch
	refuse func(t *testing.T, r lineageRig) error
}

// Defends the rollout switches: each P5 enforcement refuses when its switch
// is on, and when it is off lets the same call through and counts it as a
// would-refuse. Rules older than P5 hold either way.
func TestEachEnforcementSwitchRefusesWhenOnAndOnlyCountsWhenOff(t *testing.T) {
	rows := map[string]switchRow{
		"a key its scopes do not reach": {authz.KeyGrants, func(t *testing.T, r lineageRig) error {
			r.ownerSets(t, r.child, map[string]any{form.ScopesKey: []string{"read:*", "write:aria:self#mantra"}})
			return r.setAs(t, r.dial(t), r.child, r.child, *heldPatch(t, map[string]any{"note": "x"}))
		}},
		"an owner-held key through a label on the local door": {authz.KeyGrants, func(t *testing.T, r lineageRig) error {
			kid := r.forkAsFigaro(t, r.parent)
			return r.setLocal(t, kid, kid, map[string]any{"system.model": "cheap"})
		}},
		"a retire beyond the lineage of an unscoped figaro in a box": {authz.LineageReach, func(t *testing.T, r lineageRig) error {
			other, err := r.create(asOwner(), "judged")
			if err != nil {
				t.Fatal(err)
			}
			return r.setAs(t, r.dial(t), other.FigaroID, r.sibling, *heldPatch(t, map[string]any{"system.retired": "manual"}))
		}},
		"a fork past its quota": {authz.Quota, func(t *testing.T, r lineageRig) error {
			r.ownerSets(t, r.parent, map[string]any{form.FanoutQuotaKey: 0})
			return r.forkAs(t, r.dial(t), r.parent, r.parent, form.Patch{})
		}},
	}
	for name, row := range rows {
		for _, on := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, on %v", name, on), func(t *testing.T) {
				r := newLineageRig(t, "")
				p := r.a.Policies
				p.SetEnforcement(enforcingAllBut(row.sw, on))
				before := p.WouldRefused(row.sw)
				err := row.refuse(t, r)
				switch {
				case on && err == nil:
					t.Fatalf("%s on: the call landed", row.sw)
				case !on && err != nil:
					t.Fatalf("%s off: the call was refused: %v", row.sw, err)
				case !on && p.WouldRefused(row.sw) != before+1:
					t.Fatalf("%s off: would-refuse count %d, want %d", row.sw, p.WouldRefused(row.sw), before+1)
				case on && p.WouldRefused(row.sw) != before:
					t.Fatalf("%s on: a refusal was counted as a would-refuse", row.sw)
				}
			})
		}
	}
}

func TestAChildPolicyIsAppliedOnlyWhenItsSwitchIsOn(t *testing.T) {
	for _, on := range []bool{true, false} {
		r := newLineageRig(t, "")
		p := r.a.Policies
		p.SetEnforcement(enforcingAllBut(authz.ChildPolicy, on))
		before := p.WouldRefused(authz.ChildPolicy)
		kid := r.forkAsFigaro(t, r.parent)
		got := grantsOn(t, r, kid)
		if on && got != `["send:parent","read:aria:self","write:aria:self#@self","delete:aria:self#@self"]` {
			t.Errorf("on: a figaro's fork holds %s, want the default lane scopes", got)
		}
		if parent := grantsOn(t, r, r.parent); !on && got != parent {
			t.Errorf("off: a figaro's fork holds %s, want its parent's %s, as before child policies", got, parent)
		}
		if want := before + map[bool]uint64{true: 0, false: 1}[on]; p.WouldRefused(authz.ChildPolicy) != want {
			t.Errorf("on=%v: would-refuse count %d, want %d", on, p.WouldRefused(authz.ChildPolicy), want)
		}
	}
}

func TestASwitchOffKeepsTheRulesOlderThanIt(t *testing.T) {
	r := newLineageRig(t, "")
	r.a.Policies.SetEnforcement(authz.Enforcement{})
	kid := r.forkAsFigaro(t, r.parent)
	err := r.setLocal(t, kid, kid, map[string]any{form.ForkedFromKey: "x"})
	if err == nil || !strings.Contains(err.Error(), "only the daemon writes it") {
		t.Fatalf("with every switch off a figaro wrote a runtime key: %v", err)
	}
	c := r.dial(t)
	err = r.setAs(t, c, r.child, r.child, grantsPatch(t, []string{"*:*"}))
	if err == nil {
		t.Fatal("with every switch off a figaro set its own scopes through its box door")
	}
}

func enforcingAllBut(sw authz.Switch, on bool) authz.Enforcement {
	e := authz.Enforcement{KeyGrants: true, ChildPolicy: true, LineageReach: true, Quota: true}
	switch sw {
	case authz.KeyGrants:
		e.KeyGrants = on
	case authz.ChildPolicy:
		e.ChildPolicy = on
	case authz.LineageReach:
		e.LineageReach = on
	case authz.Quota:
		e.Quota = on
	}
	return e
}

// Defends the quota switch's reach: off, a fork past the quota lands and
// counts, and max_children, older than the quota, still refuses.
func TestWithTheQuotaOffAForkPastItCountsAgainstMaxChildren(t *testing.T) {
	r := newLineageRig(t, "")
	r.a.Policies.SetEnforcement(enforcingAllBut(authz.Quota, false))
	r.ownerSets(t, r.parent, map[string]any{form.FanoutQuotaKey: 0, form.FanoutMaxChildrenKey: 3})
	c := r.dial(t)
	if err := r.forkAs(t, c, r.parent, r.parent, form.Patch{}); err != nil {
		t.Fatalf("with the quota off a fork past it was refused: %v", err)
	}
	err := r.forkAs(t, c, r.parent, r.parent, form.Patch{})
	if err == nil || !strings.Contains(err.Error(), "max_children 3") {
		t.Fatalf("max_children stopped refusing with the quota off: %v", err)
	}
}
