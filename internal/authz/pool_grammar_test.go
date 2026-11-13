package authz

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

const brokerGroupsPolicy = `
[roles.broker-groups]
permissions = ["lease:pool:chat-groups"]

[[bind]]
role = "broker-groups"
principals = ["service:broker@auth.kelliher.info"]
`

var brokerClient = jkauthz.Principal{Kind: KindService, ID: "broker@auth.kelliher.info"}

// broker's whole grant on the pool daemon is one permission. It has to
// reach its own pool and nothing beside it: no other pool, no aria, no
// daemon call, and no verb but lease.
func TestALeaseGrantReachesOnePoolAndNoOtherVerb(t *testing.T) {
	p := loaded(t, brokerGroupsPolicy)
	s := subject(brokerClient)
	for r, want := range map[Resource]bool{
		Pool("chat-groups"):   true,
		Pool("chat-group"):    false,
		Pool(""):              false,
		Daemon:                false,
		Aria("chat-groups"):   false,
		Lease("chat-groups"):  false,
		Sandbox("worker1-g1"): false,
	} {
		if got := p.Allow(s, rpc.VerbLease, r).Allowed; got != want {
			t.Errorf("lease on %v: %v, want %v", r, got, want)
		}
	}
	for _, v := range rpc.Verbs() {
		if v != rpc.VerbLease && p.Allow(s, v, Pool("chat-groups")).Allowed {
			t.Errorf("lease:pool:chat-groups granted %s on the pool", v)
		}
	}
}

// say:lease:self is the leased box's whole role. It names the lease by
// the box that speaks, so one generation's principal cannot say into the
// next generation of the same worker.
func TestSayOnLeaseSelfReachesOnlyTheSpeakersOwnLease(t *testing.T) {
	m := selectors{}
	g2 := subject(BoxPrincipal("worker3-g2"))
	for _, c := range []struct {
		who  jkauthz.Subject
		r    Resource
		want bool
	}{
		{g2, Lease("worker3-g2"), true},
		{g2, Lease("worker3-g1"), false},
		{g2, Lease("worker3-g3"), false},
		{g2, Pool("worker3-g2"), false},
		{g2, Sandbox("worker3-g2"), false},
		{g2, Aria("worker3-g2"), false},
		{subject(brokerClient), Lease("worker3-g2"), false},
		{subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "worker3-g2@local:box"}), Lease("worker3-g2"), false},
	} {
		if got := m.Match("lease:self", c.who, c.r); got != c.want {
			t.Errorf("lease:self for %v on %v: %v, want %v", c.who.Principal, c.r, got, c.want)
		}
	}
	if m.Match("lease:worker3-g2", g2, Lease("worker3-g2")) {
		t.Error("a lease was matched by name: only lease:self names one")
	}
}

// sandboxed:<name> is spelled only by a grant the daemon computes for
// one lease. A policy file or a figaro's own scopes that could say it
// would hand out another lease's figaros by name.
func TestOnlyAComputedGrantNamesABoxedSet(t *testing.T) {
	path := writePolicy(t, strings.Replace(brokerGroupsPolicy, "lease:pool:chat-groups", "kill:sandboxed:worker3-g2", 1))
	if err := NewPolicies(me, facts()).Reload(path); err == nil {
		t.Error("a policy file bound sandboxed:worker3-g2")
	}
	if _, err := ParseFigaroGrants([]string{"kill:sandboxed:worker3-g2"}); err == nil {
		t.Error("a figaro's scopes named sandboxed:worker3-g2")
	}
	if _, err := ParseFigaroGrants([]string{"say:lease:self", "kill:sandboxed:self"}); err != nil {
		t.Errorf("the spellings a policy may use were refused: %v", err)
	}

	f := fakeFacts{boxed: map[string]string{"in-g2": "worker3-g2", "in-g1": "worker3-g1", "unboxed": ""}, unreadable: map[string]bool{"lost": true}}
	m := selectors{facts: f}
	who := subject(ProvisionerPrincipal())
	for id, want := range map[string]bool{"in-g2": true, "in-g1": false, "unboxed": false, "lost": false, "never-minted": false} {
		if got := m.Match("sandboxed:worker3-g2", who, Aria(id)); got != want {
			t.Errorf("sandboxed:worker3-g2 on %s: %v, want %v", id, got, want)
		}
	}
	for _, id := range []string{"unboxed", "never-minted", "in-g2"} {
		if m.Match("sandboxed:", who, Aria(id)) {
			t.Errorf("sandboxed: with no name matched %s", id)
		}
	}
	if m.Match("sandboxed:worker3-g2", who, Sandbox("worker3-g2")) {
		t.Error("sandboxed:<name> matched the box itself, not the figaros in it")
	}
}

// lease and say are local-only. A troupe that grants everything, on a
// node that delegates everything to it, still lends neither.
func TestATroupeLendsNeitherLeaseNorSay(t *testing.T) {
	p := NewPolicies(me, facts())
	if err := p.store.LoadBytes([]byte(`
[roles.lent]
permissions = ["*:*"]

[[bind]]
role = "lent"
principals = ["troupe:estate"]
`)); err != nil {
		t.Fatal(err)
	}
	if err := p.SetTroupe("estate", []byte(`
[roles.all]
permissions = ["*:*"]

[[bind]]
role = "all"
principals = ["user:ana@auth.kelliher.info"]
`)); err != nil {
		t.Fatal(err)
	}
	ana := subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "ana@auth.kelliher.info"})
	if !p.Allow(ana, rpc.VerbSend, Aria("aaaa0002")).Allowed {
		t.Fatal("the troupe lends nothing here, so this test would pass for the wrong reason")
	}
	for _, c := range []struct {
		verb rpc.Verb
		r    Resource
	}{
		{rpc.VerbLease, Pool("chat-groups")},
		{rpc.VerbSay, Lease("worker3-g2")},
		{rpc.VerbLease, Daemon},
		{rpc.VerbSay, Aria("aaaa0002")},
		{rpc.VerbRead, Pool("chat-groups")},
		{rpc.VerbRead, Lease("worker3-g2")},
	} {
		if p.Allow(ana, c.verb, c.r).Allowed {
			t.Errorf("the troupe lent %s on %v", c.verb, c.r)
		}
	}
}

func TestAPoolSelectorNamesOnePool(t *testing.T) {
	for sel, ok := range map[string]bool{
		"pool:chat-groups": true, "pool:": false, "pool:tele*": false,
		"lease:self": true, "lease:": false, "lease:worker3-g2": false, "lease:*": false,
		"sandboxed:self": true, "sandboxed:worker3-g2": false, "sandboxed:": false,
	} {
		if err := (selectors{}).Validate(sel); (err == nil) != ok {
			t.Errorf("%s: %v", sel, err)
		}
	}
}
