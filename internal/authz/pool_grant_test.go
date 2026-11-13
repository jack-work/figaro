package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

type leasesInForce map[string]bool

func (l leasesInForce) InForce(box string) bool { return l[box] }

const (
	leaseA = "worker1-g4"
	leaseB = "worker2-g1"
)

func poolFacts() fakeFacts {
	return fakeFacts{
		owners:     map[string]jkauthz.Principal{"owners": me},
		boxed:      map[string]string{"in-a": leaseA, "in-b": leaseB, "owners": "", "in-local": "local", "lost-in-a": leaseA},
		unreadable: map[string]bool{"lost-in-a": true},
	}
}

func poolPolicies(t *testing.T, policy string, inForce ...string) *Policies {
	t.Helper()
	p := loadedWith(t, poolFacts(), policy)
	held := leasesInForce{}
	for _, box := range inForce {
		held[box] = true
	}
	p.SetLeases(held)
	return p
}

func everyResource() []Resource {
	return []Resource{
		Daemon, Sandbox(""), Sandbox("local"), Sandbox(leaseA), Sandbox(leaseB),
		Aria("in-a"), Aria("in-b"), Aria("owners"), Aria("in-local"), Aria("lost-in-a"), Aria("never-minted"),
		Aria(rpc.AriasFormID), Aria(rpc.TopologyFormID),
		Pool("chat-groups"), Pool(leaseA), Lease(leaseA), Outfit("default"), Outfit(leaseA), Troupe("estate"),
	}
}

func heldBy(p *Policies, s jkauthz.Subject) []string {
	var held []string
	for _, v := range rpc.Verbs() {
		for _, r := range everyResource() {
			if p.Allow(s, v, r).Allowed {
				held = append(held, fmt.Sprintf("%s on %s", v, r))
			}
		}
	}
	slices.Sort(held)
	return held
}

func grantOf(box, figaro string) []string {
	want := []string{
		fmt.Sprintf("create on %s", Sandbox(box)),
		fmt.Sprintf("use on %s", Sandbox(box)),
		fmt.Sprintf("set on %s", Aria(figaro)),
		fmt.Sprintf("kill on %s", Aria(figaro)),
	}
	slices.Sort(want)
	return want
}

// D2: the provisioner's grant is worked out for the one lease its call
// names. With lease B minting at the same moment, a call serving A
// reaches A's box and A's figaros and nothing of B's, the owner's or the
// daemon's; and the same code serving B holds the mirror image.
func TestTheProvisionerHoldsTheLeaseItsCallNamesAndNoOther(t *testing.T) {
	p := poolPolicies(t, brokerGroupsPolicy, leaseA, leaseB)
	if got, want := heldBy(p, ProvisionerServing(leaseA)), grantOf(leaseA, "in-a"); !slices.Equal(got, want) {
		t.Errorf("serving %s the provisioner holds\n%s\nwant\n%s", leaseA, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := heldBy(p, ProvisionerServing(leaseB)), grantOf(leaseB, "in-b"); !slices.Equal(got, want) {
		t.Errorf("serving %s the provisioner holds\n%s\nwant\n%s", leaseB, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Ruling (a): the grant derives from the one local:pool group on the
// subject. No group, two leases, a lease named twice, or anything beside
// the lease on the subject is a call that serves no one lease, and it
// holds nothing rather than the first or the union.
func TestAProvisionerThatNamesNoLeaseOrTwoHoldsNothing(t *testing.T) {
	p := poolPolicies(t, brokerGroupsPolicy, leaseA, leaseB)
	one := ProvisionerServing(leaseA).Groups[0]
	other := ProvisionerServing(leaseB).Groups[0]
	for name, groups := range map[string][]string{
		"no group":                       nil,
		"two leases":                     {one, other},
		"one lease twice":                {one, one},
		"a lease and an issuer's group":  {one, "admins@auth.kelliher.info"},
		"an issuer's group alone":        {leaseA + "@auth.kelliher.info"},
		"the box's own scope":            {leaseA + "@" + BoxScope},
		"the bare box name":              {leaseA},
		"a lease with no name":           {"@" + PoolScope},
		"two leases in one group's text": {leaseA + "@" + PoolScope + "," + other},
	} {
		s := jkauthz.Subject{Principal: ProvisionerPrincipal(), Groups: groups}
		if held := heldBy(p, s); len(held) != 0 {
			t.Errorf("%s: the provisioner holds %v", name, held)
		}
	}
	if held := heldBy(p, ProvisionerServing(leaseA)); len(held) == 0 {
		t.Fatal("the provisioner holds nothing with one lease either, so the rows above prove nothing")
	}
}

// The grant lives exactly as long as the ledger row: minting, live or
// releasing. A lease that was released, one that never was, and a daemon
// that was given no ledger to ask all leave the provisioner with nothing.
func TestTheProvisionersGrantEndsWithItsLease(t *testing.T) {
	p := poolPolicies(t, brokerGroupsPolicy, leaseB)
	if held := heldBy(p, ProvisionerServing(leaseA)); len(held) != 0 {
		t.Errorf("serving a lease that is not in force the provisioner holds %v", held)
	}
	if held := heldBy(p, ProvisionerServing(leaseB)); len(held) == 0 {
		t.Error("the lease in force gave the provisioner nothing")
	}
	unasked := loadedWith(t, poolFacts(), brokerGroupsPolicy)
	if held := heldBy(unasked, ProvisionerServing(leaseA)); len(held) != 0 {
		t.Errorf("with no ledger to ask the provisioner holds %v", held)
	}
}

// The grant is computed, never read: a policy that gives every service
// everything, and a troupe that lends everything, add nothing to it, and
// the same subject under another kind or node is not the provisioner.
func TestNoPolicyOrTroupeWidensTheProvisioner(t *testing.T) {
	p := poolPolicies(t, `
[roles.all]
permissions = ["*:*"]

[[bind]]
role = "all"
principals = ["service:*", "troupe:estate", "service:provisioner@local:pool"]
groups = ["`+leaseA+`@local:pool"]
`, leaseA)
	if err := p.SetTroupe("estate", []byte(`
[roles.all]
permissions = ["*:*"]

[[bind]]
role = "all"
principals = ["service:provisioner@local:pool"]
`)); err != nil {
		t.Fatal(err)
	}
	stranger := jkauthz.Subject{Principal: jkauthz.Principal{Kind: KindService, ID: "broker@auth.kelliher.info"}}
	if !p.Allow(stranger, rpc.VerbSend, Aria("owners")).Allowed {
		t.Fatal("the policy gives a service nothing, so this test would pass for the wrong reason")
	}
	if got, want := heldBy(p, ProvisionerServing(leaseA)), grantOf(leaseA, "in-a"); !slices.Equal(got, want) {
		t.Errorf("under a policy that gives services everything the provisioner holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if held := heldBy(p, jkauthz.Subject{Principal: ProvisionerPrincipal()}); len(held) != 0 {
		t.Errorf("with no lease, under that policy, the provisioner holds %v", held)
	}
}

func guardedAsProvisioner(t *testing.T, p *Policies) (map[string]jkrpc.HandlerFunc, func(box string) context.Context) {
	t.Helper()
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	handlers := map[string]jkrpc.HandlerFunc{}
	for _, m := range rpc.GuardedMethods() {
		handlers[m] = ok
	}
	p.SetPoolDaemon(true)
	return Guard(handlers, AriaHeader{}, Enforce(p)), func(box string) context.Context {
		return WithDoor(WithSubject(context.Background(), ProvisionerServing(box)), DoorInProcess)
	}
}

// Ruling (b): the guard is not where the provisioner creates. Its grant
// says create on sandbox:B and the guard asks create on the daemon, so
// figaro.create as the provisioner is refused there, and the one mint
// function that asks Policies.Allow about the dressed form's box is the
// only way it bears a figaro.
func TestTheGuardRefusesTheProvisionerACreate(t *testing.T) {
	p := poolPolicies(t, brokerGroupsPolicy, leaseA)
	h, serving := guardedAsProvisioner(t, p)
	for _, params := range []string{`{}`, `{"outfits":["leased"]}`, `{"patch":{"system":{"sandbox":"` + leaseA + `"}}}`, `{"figaro_id":"in-a"}`} {
		if _, err := h[rpc.MethodCreate](serving(leaseA), json.RawMessage(params)); err == nil {
			t.Errorf("%s %s as the provisioner serving %s was served by the guard", rpc.MethodCreate, params, leaseA)
		}
	}
	if !p.Allow(ProvisionerServing(leaseA), rpc.VerbCreate, Sandbox(leaseA)).Allowed {
		t.Error("the mint function's own question, create on the lease's box, was refused")
	}
	for _, born := range []string{"", "local", leaseB} {
		if p.Allow(ProvisionerServing(leaseA), rpc.VerbCreate, Sandbox(born)).Allowed {
			t.Errorf("D3: a birth whose form says sandbox %q passed create:sandbox:%s", born, leaseA)
		}
	}
}

// R2, D2 at the guard: serving lease A, the provisioner is refused every
// guarded method against the owner's figaro, lease B's figaro and the
// daemon, the ones other daemons leave to their handlers among them.
// Against A's own figaro it passes the methods whose verb is set or kill
// and no other, so it cannot read, prompt or fork what it minted.
func TestTheGuardRefusesTheProvisionerEveryMethodOutsideItsLease(t *testing.T) {
	p := poolPolicies(t, brokerGroupsPolicy, leaseA, leaseB)
	h, serving := guardedAsProvisioner(t, p)
	ctx := serving(leaseA)
	var unverbed []string
	for _, m := range rpc.GuardedMethods() {
		verb, _ := rpc.VerbOf(m)
		if verb == rpc.VerbNone {
			unverbed = append(unverbed, m)
			continue
		}
		for _, aria := range []string{"owners", "in-b", "in-local", "never-minted", ""} {
			if err := call(t, h, ctx, m, aria); err == nil {
				t.Errorf("%s on %q was served to the provisioner serving %s", m, aria, leaseA)
			}
		}
		for _, params := range []string{`{"pool":"chat-groups"}`, `{"pool":"` + leaseA + `"}`} {
			if _, err := h[m](ctx, json.RawMessage(params)); err == nil {
				t.Errorf("%s %s was served to the provisioner serving %s", m, params, leaseA)
			}
		}
		err := call(t, h, ctx, m, "in-a")
		if own := verb == rpc.VerbSet || verb == rpc.VerbKill; own != (err == nil) {
			t.Errorf("%s (%s) on lease %s's own figaro: served %v, want %v (%v)", m, verb, leaseA, err == nil, own, err)
		}
	}
	if want := []string{rpc.MethodHello}; !slices.Equal(unverbed, want) {
		t.Errorf("methods with no verb, which the guard serves any in-process caller: %v, want %v", unverbed, want)
	}
}
