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
