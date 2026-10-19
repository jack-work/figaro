package authz

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func BenchmarkPoliciesAllow(b *testing.B) {
	p := NewPolicies(me, facts())
	if err := p.store.LoadBytes([]byte(sandboxPolicy)); err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name string
		who  jkauthz.Principal
		verb rpc.Verb
		r    Resource
	}{
		{"owner-wildcard", me, rpc.VerbSend, Aria("aaaa0002")},
		{"sandbox-owner-self", box, rpc.VerbSend, Aria("aaaa0001")},
		{"sandbox-lineage", box, rpc.VerbRead, Aria("cccc0003")},
		{"sandbox-denied", box, rpc.VerbKill, Aria("aaaa0001")},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			s := subject(c.who)
			b.ReportAllocs()
			for b.Loop() {
				p.Allow(s, c.verb, c.r)
			}
		})
	}
}

func BenchmarkPoliciesAllowTroupe(b *testing.B) {
	p := NewPolicies(me, facts())
	if err := p.store.LoadBytes([]byte(sandboxPolicy + `
[roles.lent]
grants = [{ verbs = ["read", "listen", "send"], on = ["*"] }]

[[bind]]
role = "lent"
principals = ["troupe:estate"]
`)); err != nil {
		b.Fatal(err)
	}
	if err := p.SetTroupe("estate", []byte(`
[roles.all]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "all"
principals = ["user:ana@auth.kelliher.info"]
`)); err != nil {
		b.Fatal(err)
	}
	ana := jkauthz.Principal{Kind: jkauthz.KindUser, ID: "ana@auth.kelliher.info"}
	cases := []struct {
		name string
		who  jkauthz.Principal
		verb rpc.Verb
		r    Resource
	}{
		{"local-hit", me, rpc.VerbSend, Aria("aaaa0002")},
		{"troupe-hit", ana, rpc.VerbSend, Aria("aaaa0002")},
		{"troupe-undelegated", ana, rpc.VerbKill, Aria("aaaa0002")},
		{"miss", other, rpc.VerbSend, Aria("aaaa0002")},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			s := subject(c.who)
			b.ReportAllocs()
			for b.Loop() {
				p.Allow(s, c.verb, c.r)
			}
		})
	}
}
