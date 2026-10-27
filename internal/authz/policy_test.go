package authz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

type fakeFacts struct {
	owners  map[string]jkauthz.Principal
	parents map[string]string
	roles   map[string]string
	boxed   map[string]string
}

func (f fakeFacts) Owner(id string) (jkauthz.Principal, bool) {
	p, ok := f.owners[id]
	return p, ok
}

func (f fakeFacts) DescendsFrom(id, root string) bool {
	for p := f.parents[id]; p != ""; p = f.parents[p] {
		if p == root {
			return true
		}
	}
	return false
}

func (f fakeFacts) RoleTarget(role string) string { return f.roles[role] }

func (f fakeFacts) SandboxOf(id string) string { return f.boxed[id] }

var (
	me    = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}
	box   = jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "dev-3"}
	other = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1001"}
)

func facts() fakeFacts {
	return fakeFacts{
		owners:  map[string]jkauthz.Principal{"aaaa0001": box, "aaaa0002": me},
		parents: map[string]string{"cccc0002": "cccc0001", "cccc0003": "cccc0002"},
		roles:   map[string]string{"@herald": "bbbb0001"},
	}
}

func subject(p jkauthz.Principal) jkauthz.Subject { return jkauthz.Subject{Principal: p} }

func loaded(t *testing.T, src string) *Policies {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(me, facts())
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	return p
}

// The default is what a desktop runs with no policy file: the daemon's own
// uid may do everything, including use:local, and nobody else may do
// anything. That is today's behaviour with the door closed to strangers.
func TestTheDefaultPolicyIsTheDaemonsOwnUidAndNobodyElse(t *testing.T) {
	p := NewPolicies(me, facts())
	if err := p.Reload(""); err != nil {
		t.Fatal(err)
	}
	for _, v := range rpc.Verbs() {
		if d := p.Allow(subject(me), v, Aria("aaaa0001")); !d.Allowed {
			t.Errorf("self %s: %s", v, d.Reason())
		}
	}
	if d := p.Allow(subject(me), rpc.VerbUse, Sandbox("local")); !d.Allowed {
		t.Errorf("self use:local: %s", d.Reason())
	}
	if d := p.Allow(subject(other), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Error("another uid reads under the default")
	}
	if d := p.Allow(subject(box), rpc.VerbUse, Sandbox("local")); d.Allowed {
		t.Error("a sandbox may use local under the default")
	}
}

const sandboxPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.box]
grants = [
  { verbs = ["read", "listen", "send", "set"], on = ["owner:self", "role:@herald", "lineage:cccc0001"] },
  { verbs = ["create"], on = ["*"] },
  { verbs = ["use"], on = ["sandbox:dev-3"] },
]

[[bind]]
role = "owner"
principals = ["user:1000"]

[[bind]]
role = "box"
principals = ["sandbox:dev-3"]
`

func TestSelectorsMatchFigarosResources(t *testing.T) {
	p := loaded(t, sandboxPolicy)
	cases := []struct {
		name string
		r    Resource
		verb rpc.Verb
		want bool
	}{
		{"an aria it minted", Aria("aaaa0001"), rpc.VerbSend, true},
		{"an aria someone else minted", Aria("aaaa0002"), rpc.VerbSend, false},
		{"an aria nobody recorded minting", Aria("dddd0001"), rpc.VerbSend, false},
		{"the aria a role points at", Aria("bbbb0001"), rpc.VerbSend, true},
		{"the root of a granted lineage", Aria("cccc0001"), rpc.VerbRead, true},
		{"a grandchild in that lineage", Aria("cccc0003"), rpc.VerbRead, true},
		{"its own sandbox", Sandbox("dev-3"), rpc.VerbUse, true},
		{"local", Sandbox("local"), rpc.VerbUse, false},
		{"the unnamed sandbox, which is local", Sandbox(""), rpc.VerbUse, false},
		{"a verb outside its role", Aria("aaaa0001"), rpc.VerbKill, false},
		{"a daemon-wide verb", Daemon, rpc.VerbAdmin, false},
		{"create, granted on anything", Daemon, rpc.VerbCreate, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := p.Allow(subject(box), c.verb, c.r)
			if d.Allowed != c.want {
				t.Fatalf("want %v: %s", c.want, d.Reason())
			}
		})
	}
}

func TestASandboxSelectorNeverMatchesAnAria(t *testing.T) {
	p := loaded(t, `
[roles.r]
grants = [{ verbs = ["read"], on = ["sandbox:dev-3"] }]
[[bind]]
role = "r"
principals = ["sandbox:dev-3"]
`)
	if d := p.Allow(subject(box), rpc.VerbRead, Aria("dev-3")); d.Allowed {
		t.Fatal(d.Reason())
	}
}

// join is granted per troupe: a grant on one troupe admits nobody to
// another, a troupe selector matches no aria, and the owner's default
// grant covers it.
func TestJoinIsGrantedOnANamedTroupe(t *testing.T) {
	p := loaded(t, `
[roles.member]
grants = [{ verbs = ["join"], on = ["troupe:estate"] }, { verbs = ["read"], on = ["troupe:estate"] }]
[[bind]]
role = "member"
principals = ["user:1001"]
`)
	cases := []struct {
		name string
		r    Resource
		verb rpc.Verb
		want bool
	}{
		{"its troupe", Troupe("estate"), rpc.VerbJoin, true},
		{"another troupe", Troupe("other"), rpc.VerbJoin, false},
		{"an aria named like the troupe", Aria("estate"), rpc.VerbRead, false},
		{"a sandbox named like the troupe", Sandbox("estate"), rpc.VerbJoin, false},
		{"the daemon", Daemon, rpc.VerbJoin, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if d := p.Allow(subject(other), c.verb, c.r); d.Allowed != c.want {
				t.Fatalf("want %v: %s", c.want, d.Reason())
			}
		})
	}
	def := NewPolicies(me, facts())
	if err := def.Reload(""); err != nil {
		t.Fatal(err)
	}
	if d := def.Allow(subject(me), rpc.VerbJoin, Troupe("estate")); !d.Allowed {
		t.Fatalf("the owner cannot join under the default policy: %s", d.Reason())
	}
	if d := def.Allow(subject(other), rpc.VerbJoin, Troupe("estate")); d.Allowed {
		t.Fatal("a stranger may join under the default policy")
	}
}

func TestSelectorsFigaroDoesNotKnowAreRefusedAtLoad(t *testing.T) {
	for _, sel := range []string{"owner:me", "role:herald", "aria:", "lineage:", "sandbox:", "troupe:", "path:/srv"} {
		p := NewPolicies(me, facts())
		err := p.Reload(writePolicy(t, `
[roles.r]
grants = [{ verbs = ["read"], on = ["`+sel+`"] }]`))
		if err == nil {
			t.Errorf("%q loaded", sel)
		}
	}
}

func writePolicy(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A configured policy that cannot be read must not fall back to the
// permissive default: the daemon serves nobody and says why.
func TestABrokenPolicyAtBootDeniesEveryone(t *testing.T) {
	p := NewPolicies(me, facts())
	err := p.Reload(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "missing.toml") {
		t.Fatalf("err %v", err)
	}
	if d := p.Allow(subject(me), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Fatal("a broken boot fell back to allowing the owner")
	}
}

func TestReloadChangesTheAnswerAndAFailedReloadKeepsTheOld(t *testing.T) {
	path := writePolicy(t, sandboxPolicy)
	p := NewPolicies(me, facts())
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	if d := p.Allow(subject(box), rpc.VerbKill, Aria("aaaa0001")); d.Allowed {
		t.Fatal("kill before the grant")
	}

	if err := os.WriteFile(path, []byte(sandboxPolicy+`
[[bind]]
role = "owner"
principals = ["sandbox:dev-3"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	if d := p.Allow(subject(box), rpc.VerbKill, Aria("aaaa0001")); !d.Allowed {
		t.Fatalf("kill after the grant: %s", d.Reason())
	}

	if err := os.WriteFile(path, []byte("[roles.broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err == nil {
		t.Fatal("loaded garbage")
	}
	if d := p.Allow(subject(box), rpc.VerbKill, Aria("aaaa0001")); !d.Allowed {
		t.Fatalf("a failed reload dropped the serving policy: %s", d.Reason())
	}
}

func TestReloadingWithNoPathRestoresTheDefault(t *testing.T) {
	p := NewPolicies(me, facts())
	if err := p.Reload(writePolicy(t, "")); err != nil {
		t.Fatal(err)
	}
	if d := p.Allow(subject(me), rpc.VerbRead, Daemon); d.Allowed {
		t.Fatal("an empty policy file allowed the owner")
	}
	if err := p.Reload(""); err != nil {
		t.Fatal(err)
	}
	if d := p.Allow(subject(me), rpc.VerbRead, Daemon); !d.Allowed {
		t.Fatal(d.Reason())
	}
}
