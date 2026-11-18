package authz

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func treeFakes(own map[string]string) figaroFakes {
	f := facts()
	f.parents = map[string]string{"kid": "top", "grandkid": "kid", "sib": "top", "niece": "sib"}
	f.roles = map[string]string{"@role-top": "top", "@role-sib": "sib"}
	return figaroFakes{
		fakeFacts: f,
		chains: map[string][]string{
			"top": {"top"}, "kid": {"top", "kid"}, "grandkid": {"top", "kid", "grandkid"},
			"sib": {"top", "sib"}, "niece": {"top", "sib", "niece"},
		},
		own: own,
	}
}

func viewOf(own map[string]string, figaro string) FigaroView {
	return NewPolicies(me, treeFakes(own)).Figaro(figaro)
}

// The matrix the per-key write check defends: writer x target x key x
// selector. kid is the writer throughout; its parent is top, its child is
// grandkid, sib is its sibling.
func TestKeyGrantMatrix(t *testing.T) {
	type cell struct {
		scopes string
		verb   rpc.Verb
		target string
		key    string
		want   bool
	}
	cells := []cell{
		{`["set:*"]`, rpc.VerbSet, "sib", "mantra", true},
		{`["set:*"]`, rpc.VerbDelete, "sib", "mantra", true},
		{`["write:*"]`, rpc.VerbSet, "sib", "mantra", true},
		{`["write:*"]`, rpc.VerbDelete, "sib", "mantra", false},
		{`["delete:*"]`, rpc.VerbDelete, "sib", "mantra", true},
		{`["write:*#mantra"]`, rpc.VerbSet, "sib", "mantra", true},
		{`["write:*#mantra"]`, rpc.VerbSet, "sib", "brief", false},
		{`["write:*#system.thinking_*"]`, rpc.VerbSet, "sib", "system.thinking_effort", true},
		{`["write:*#system.thinking_*"]`, rpc.VerbSet, "sib", "system.thinking_effort.x", false},
		{`["write:*#system.thinking_*"]`, rpc.VerbSet, "sib", "system.model", false},
		{`["write:*#system.**"]`, rpc.VerbSet, "sib", "system.decide.model", true},
		{`["write:*#system.**"]`, rpc.VerbSet, "sib", "mantra", false},
		{`["write:parent"]`, rpc.VerbSet, "top", "mantra", true},
		{`["write:parent"]`, rpc.VerbSet, "sib", "mantra", false},
		{`["write:parent"]`, rpc.VerbSet, "grandkid", "mantra", false},
		{`["write:parent#comments.*"]`, rpc.VerbSet, "top", "comments.a", true},
		{`["write:parent#comments.*"]`, rpc.VerbSet, "top", "mantra", false},
		{`["write:children"]`, rpc.VerbSet, "grandkid", "mantra", true},
		{`["write:children"]`, rpc.VerbSet, "kid", "mantra", false},
		{`["write:children"]`, rpc.VerbSet, "top", "mantra", false},
		{`["write:children"]`, rpc.VerbSet, "niece", "mantra", false},
		{`["write:lineage:self"]`, rpc.VerbSet, "kid", "mantra", true},
		{`["write:lineage:self"]`, rpc.VerbSet, "grandkid", "mantra", true},
		{`["write:aria:self#mantra"]`, rpc.VerbSet, "kid", "mantra", true},
		{`["write:aria:self#mantra"]`, rpc.VerbSet, "grandkid", "mantra", false},
		{`["read:aria:self"]`, rpc.VerbSet, "kid", "mantra", false},
		{`["write:aria:sib"]`, rpc.VerbSet, "sib", "anything.at.all", true},
		{`["write:aria:sib"]`, rpc.VerbSet, "niece", "mantra", false},
	}
	for _, c := range cells {
		v := viewOf(map[string]string{"kid": c.scopes}, "kid")
		if got := v.AllowsKey(jkauthz.Subject{Principal: me}, c.verb, Aria(c.target), c.key); got != c.want {
			t.Errorf("%s %s %s on %s: %v, want %v", c.scopes, c.verb, c.key, c.target, got, c.want)
		}
	}
}

func TestAKeyedGrantNarrowsDownTheTreeLikeAnyOther(t *testing.T) {
	own := map[string]string{
		"kid":      `["write:lineage:self#comments.**"]`,
		"grandkid": `["write:aria:self"]`,
	}
	v := viewOf(own, "grandkid")
	s := jkauthz.Subject{Principal: me}
	if !v.AllowsKey(s, rpc.VerbSet, Aria("grandkid"), "comments.x") {
		t.Error("the meet lost the key glob both sides allow")
	}
	if v.AllowsKey(s, rpc.VerbSet, Aria("grandkid"), "mantra") {
		t.Error("a child widened past its parent's key glob")
	}
	if got := effectiveOf(t, treeFakes(own), "grandkid"); got != "set:aria:grandkid#comments.**" {
		t.Errorf("effective %s", got)
	}
}

func TestParentAndChildrenResolvePerFigaro(t *testing.T) {
	own := map[string]string{"kid": `["send:parent", "read:children"]`}
	if got := effectiveOf(t, treeFakes(own), "kid"); got != "read:below:kid, send:aria:top" {
		t.Errorf("kid: %s", got)
	}
	if got := effectiveOf(t, treeFakes(map[string]string{"top": `["send:parent"]`}), "top"); got != "" {
		t.Errorf("a top-level figaro has no parent to send to, got %q", got)
	}
}

func TestADefaultChildReachesItsParentAndNothingElse(t *testing.T) {
	v := viewOf(map[string]string{"kid": `["send:parent", "read:aria:self"]`}, "kid")
	s := jkauthz.Subject{Principal: me}
	for target, want := range map[string]bool{"top": true, "@role-top": true, "sib": false, "@role-sib": false, "grandkid": false, "kid": false} {
		if got := v.refuses(s, rpc.VerbSend, Aria(target)) == ""; got != want {
			t.Errorf("send to %s: %v, want %v", target, got, want)
		}
	}
	if v.refuses(s, rpc.VerbRead, Aria("kid")) != "" || v.refuses(s, rpc.VerbRead, Aria("top")) == "" {
		t.Error("read reaches past its own form")
	}
	if v.refuses(s, rpc.VerbSet, Aria("@role-top")) == "" {
		t.Error("a role that resolves to the parent counts as the parent for set, not only send")
	}
}

func TestAChildWithoutSendParentCannotReachIt(t *testing.T) {
	v := viewOf(map[string]string{"kid": `["read:aria:self"]`}, "kid")
	if v.refuses(jkauthz.Subject{Principal: me}, rpc.VerbSend, Aria("top")) == "" {
		t.Error("reached the parent without send:parent")
	}
}

func TestAKeyedReadDoesNotOpenTheWholeForm(t *testing.T) {
	v := viewOf(map[string]string{"kid": `["read:aria:sib#comments.*"]`}, "kid")
	s := jkauthz.Subject{Principal: me}
	if v.refuses(s, rpc.VerbRead, Aria("sib")) == "" {
		t.Error("a keyed read passed a whole-form read")
	}
	all, globs := v.KeyGlobs(s, rpc.VerbRead, Aria("sib"))
	if all || strings.Join(globs, ",") != "comments.*" {
		t.Errorf("globs %v all %v", globs, all)
	}
}

func TestAWriterGrantsOnlyWhatItHoldsKeysIncluded(t *testing.T) {
	p := NewPolicies(me, treeFakes(map[string]string{"kid": `["write:children#comments.**", "send:parent"]`}))
	for want, ok := range map[string]bool{
		`["write:aria:self#comments.a"]`:  true,
		`["write:aria:self#comments.**"]`: true,
		`["write:aria:self"]`:             false,
		`["write:aria:self#mantra"]`:      false,
		`["send:parent"]`:                 false,
		`["send:aria:kid"]`:               false,
		`["set:aria:self#comments.a"]`:    false,
	} {
		gs, err := DecodeFigaroGrants([]byte(want))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.HoldsGrants("kid", gs, "grandkid", ""); (err == nil) != ok {
			t.Errorf("kid gives grandkid %s: %v, want ok=%v", want, err, ok)
		}
	}
}

func TestKeyGrantGrammar(t *testing.T) {
	for text, bad := range map[string]bool{
		"write:*#system.**":                  false,
		"set:lineage:self#system.thinking_*": false,
		"read:parent#brief":                  false,
		"delete:children":                    false,
		"send:parent":                        false,
		"set:aria:abc":                       false,
		"write:*#":                           true,
		"write:*#a..b":                       true,
		"write:*#a.b**":                      true,
		"write:*#runtime.retired":            true,
		"write:*#runtime.**":                 true,
		"write:*#runtime":                    true,
		"write:below:abc":                    true,
	} {
		_, err := ParseFigaroGrants([]string{text})
		if (err != nil) != bad {
			t.Errorf("%s: %v", text, err)
		}
	}
	gs, _ := ParseFigaroGrants([]string{"set:aria:abc"})
	if got := gs.String(); got != "set:aria:abc, delete:aria:abc" {
		t.Errorf("old set means write and delete: %s", got)
	}
	gs, _ = ParseFigaroGrants([]string{"write:aria:abc"})
	if got := gs.String(); got != "set:aria:abc" {
		t.Errorf("write is set's new name: %s", got)
	}
}

func BenchmarkAPatchCheckedKeyByKey(b *testing.B) {
	p := NewPolicies(me, treeFakes(map[string]string{
		"kid":      `["read:*", "send:parent", "write:children#comments.**", "write:children#mantra", "delete:children#mantra"]`,
		"grandkid": `["write:aria:self#comments.**", "write:aria:self#mantra"]`,
	}))
	s := jkauthz.Subject{Principal: me}
	keys := []string{"mantra", "comments.a", "comments.b.c", "mantra", "comments.d"}
	b.ReportAllocs()
	for b.Loop() {
		v := p.Figaro("grandkid")
		for _, k := range keys {
			if !v.AllowsKey(s, rpc.VerbSet, Aria("grandkid"), k) {
				b.Fatal(k)
			}
		}
	}
}

func TestTopologyVerbsReachOnlyTheFigarosOwnLineageUnlessGranted(t *testing.T) {
	s := jkauthz.Subject{Principal: me}
	p := NewPolicies(me, treeFakes(nil))
	for target, ok := range map[string]bool{"kid": true, "grandkid": true, "sib": false, "niece": false, "top": false} {
		if err := p.TopologyReach(s, "kid", rpc.VerbKill, target); (err == nil) != ok {
			t.Errorf("unscoped kid kills %s: %v, want ok=%v", target, err, ok)
		}
	}
	granted := NewPolicies(me, treeFakes(map[string]string{"kid": `["kill:*", "read:*"]`}))
	if err := granted.TopologyReach(s, "kid", rpc.VerbKill, "niece"); err != nil {
		t.Errorf("a written kill:* does not reach the fleet: %v", err)
	}
	if err := granted.TopologyReach(s, "kid", rpc.VerbSet, "niece"); err == nil {
		t.Error("kill:* let it retire (set) elsewhere")
	}
	if err := p.TopologyReach(s, "", rpc.VerbKill, "niece"); err != nil {
		t.Errorf("a call naming no figaro is judged by its principal alone: %v", err)
	}
}
