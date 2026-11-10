package authz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

func narrowed(t *testing.T, policy string, own map[string]string) *Policies {
	t.Helper()
	f := lineageFakes(own)
	f.owners["kid"] = me
	p := NewPolicies(me, f)
	if err := p.load([]byte(policy)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAFigaroCallIsTheCallersGrantsIntersectedWithTheFigaros(t *testing.T) {
	p := narrowed(t, sandboxPolicy, map[string]string{"top": `["read:*", "send:aria:aaaa0002", "kill:lineage:self"]`})
	for _, c := range []struct {
		name   string
		figaro string
		verb   rpc.Verb
		on     Resource
		want   bool
	}{
		{"read anything", "kid", rpc.VerbRead, Aria("aaaa0001"), true},
		{"send where granted", "kid", rpc.VerbSend, Aria("aaaa0002"), true},
		{"send elsewhere", "kid", rpc.VerbSend, Aria("aaaa0001"), false},
		{"kill inside the top's lineage", "kid", rpc.VerbKill, Aria("grandkid"), true},
		{"kill outside it", "kid", rpc.VerbKill, Aria("aaaa0001"), false},
		{"admin the daemon", "kid", rpc.VerbAdmin, Daemon, false},
		{"no claim: the principal alone", "", rpc.VerbAdmin, Daemon, true},
		{"a figaro not held here: the principal alone", "elsewhere", rpc.VerbAdmin, Daemon, true},
	} {
		d := p.AllowFigaro(subject(me), c.figaro, c.verb, c.on)
		if d.Allowed != c.want {
			t.Errorf("%s: allowed=%v (%s)", c.name, d.Allowed, d.Reason())
		}
		if !d.Allowed && (!d.Narrowed() || !strings.Contains(d.Reason(), "figaro:"+c.figaro)) {
			t.Errorf("%s: the refusal does not name the figaro: %s", c.name, d.Reason())
		}
	}
	if d := p.AllowFigaro(subject(other), "kid", rpc.VerbRead, Aria("aaaa0001")); d.Allowed {
		t.Error("a figaro's grants widened a principal that holds no role")
	}
}

func TestTheCeilingBoundsEveryTopLevelFigaro(t *testing.T) {
	p := narrowed(t, sandboxPolicy+"\n[figaros]\ngrants = [\"read:*\", \"*:lineage:self\", \"egress:*\"]\n", map[string]string{"top": `["*:*", "egress:api:443", "cred:github"]`})
	top, _ := p.Effective("top")
	if got := top.String(); got != "*:lineage:top, read:*, egress:api:443" {
		t.Fatalf("top: %s", got)
	}
	kid, _ := p.Effective("kid")
	if got := kid.String(); got != "*:lineage:top, read:*" {
		t.Fatalf("kid: %s", got)
	}
	if d := p.AllowFigaro(subject(me), "top", rpc.VerbSend, Aria("aaaa0002")); d.Allowed {
		t.Error("the ceiling let a top-level figaro send outside its lineage")
	}
	if d := p.AllowFigaro(subject(me), "top", rpc.VerbKill, Aria("grandkid")); !d.Allowed {
		t.Errorf("lineage:self: %s", d.Reason())
	}
	if d := p.Allow(subject(me), rpc.VerbSend, Aria("aaaa0002")); !d.Allowed {
		t.Error("the ceiling narrowed an unclaimed call")
	}
}

func TestTheCeilingTableIsChecked(t *testing.T) {
	for name, table := range map[string]string{
		"no grants":    "[figaros]\n",
		"unknown key":  "[figaros]\ngrants = [\"read:*\"]\nverbs = [\"read\"]\n",
		"bad grant":    "[figaros]\ngrants = [\"read\"]\n",
		"bad selector": "[figaros]\ngrants = [\"read:aria:*\"]\n",
		"not a table":  "figaros = 1\n",
	} {
		p := NewPolicies(me, facts())
		if err := p.load([]byte(sandboxPolicy + "\n" + table)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	p := NewPolicies(me, facts())
	if err := p.load([]byte(sandboxPolicy + "\n[figaros]\ngrants = []\n")); err != nil {
		t.Fatalf("an empty ceiling: %v", err)
	}
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	if _, set := p.ceiling(); set {
		t.Fatal("a reload without [figaros] kept the old ceiling")
	}
}

func boxClaimRig(t *testing.T) (func(figaro string, method string, on string) (string, error), *Policies) {
	t.Helper()
	f := lineageFakes(map[string]string{"in-judge": `["read:*"]`})
	f.boxed = map[string]string{"in-judge": "judge", "in-other": "other", "local-one": "local"}
	for _, id := range []string{"in-judge", "in-other", "local-one"} {
		f.chains[id] = []string{id}
	}
	p := NewPolicies(me, f)
	if err := p.load([]byte(DefaultPolicy(me, []string{"judge"}))); err != nil {
		t.Fatal(err)
	}
	var seen string
	ok := func(ctx context.Context, _ json.RawMessage) (any, error) { seen = FigaroOf(ctx); return nil, nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodIR: ok, rpc.MethodKill: ok}, AriaHeader{}, Enforce(p))
	ctx := WithDoor(WithSubject(context.Background(), jkauthz.Subject{Principal: BoxPrincipal("judge")}), DoorBox)
	return func(figaro, method, on string) (string, error) {
		seen = "unreached"
		params, _ := json.Marshal(map[string]any{"figaro_id": on})
		if figaro != "" {
			params = stamped(t, on, figaro)
		}
		_, err := Authenticated(AriaHeader{}, h[method])(ctx, params)
		return seen, err
	}, p
}

func TestABoxClaimCountsOnlyForAFigaroInThatBox(t *testing.T) {
	call, p := boxClaimRig(t)
	if seen, err := call("made-up", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("a made-up claim must fall back to the box: seen %q, %v", seen, err)
	}
	if seen, err := call("in-other", rpc.MethodKill, "in-judge"); err != nil || seen != "" {
		t.Fatalf("another box's figaro: seen %q, %v", seen, err)
	}
	if _, err := call("in-judge", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro:in-judge") {
		t.Fatalf("the box's own figaro is narrowed by its grants: %v", err)
	}
	if seen, err := call("in-judge", rpc.MethodIR, "in-judge"); err != nil || seen != "in-judge" {
		t.Fatalf("the box's own figaro reads: seen %q, %v", seen, err)
	}

	p.SetFigaroClaimRequired(true)
	for _, figaro := range []string{"", "made-up", "in-other", "local-one"} {
		if _, err := call(figaro, rpc.MethodIR, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro_claim") {
			t.Errorf("required, claim %q: %v", figaro, err)
		}
	}
	if _, err := call("in-judge", rpc.MethodIR, "in-judge"); err != nil {
		t.Fatalf("required, own figaro: %v", err)
	}
	if _, err := call("in-judge", rpc.MethodKill, "in-judge"); err == nil || !strings.Contains(err.Error(), "figaro:in-judge") {
		t.Fatalf("required, own figaro, narrowed: %v", err)
	}
}

func TestTheClaimRequirementStaysOnTheBoxDoor(t *testing.T) {
	p := NewPolicies(me, facts())
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	p.SetFigaroClaimRequired(true)
	ok := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	call := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: ok}, AriaHeader{}, Enforce(p))[rpc.MethodQua]
	plain, _ := json.Marshal(map[string]any{"figaro_id": "aaaa0001"})
	if _, err := call(WithSubject(context.Background(), subject(me)), plain); err != nil {
		t.Fatalf("the requirement reached another door: %v", err)
	}
}
