package authz

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const narrowRoles = `
[roles.owner]
permissions = ["*:*"]

[[bind]]
role = "owner"
principals = ["user:1000"]

[roles.reader]
permissions = ["read:aria:aaaa0001"]

[[bind]]
role = "reader"
groups = ["readers@auth.kelliher.info"]
`

// A role counts whatever it grants: a binding to one aria's read seats its
// holder as surely as the owner's binding does, by principal or by group.
func TestHoldsRoleIsAnyBindingByPrincipalOrGroup(t *testing.T) {
	p := loaded(t, narrowRoles)
	inGroup := jkauthz.Subject{Principal: guest, Groups: []string{"readers@auth.kelliher.info"}}
	for name, c := range map[string]struct {
		s    jkauthz.Subject
		want bool
	}{
		"the owner by principal":   {subject(me), true},
		"a reader by group":        {inGroup, true},
		"a stranger":               {subject(other), false},
		"the zero subject":         {jkauthz.Subject{}, false},
		"a group the policy skips": {jkauthz.Subject{Principal: guest, Groups: []string{"readers"}}, false},
	} {
		if got := p.HoldsRole(c.s); got != c.want {
			t.Errorf("%s: HoldsRole = %v, want %v", name, got, c.want)
		}
	}
}

// A troupe binding seats a subject only where this node lends the troupe a
// role; a troupe document that does not load seats nobody.
func TestATroupeBindingHoldsARoleOnlyWhereTheNodeDelegates(t *testing.T) {
	if p := withTroupe(t, localDelegating(t, `"read:*"`), everything); !p.HoldsRole(subject(guest)) {
		t.Fatal("a troupe-bound subject on a delegating node holds no role")
	}
	if p := withTroupe(t, loaded(t, narrowRoles), everything); p.HoldsRole(subject(guest)) {
		t.Fatal("a node that lends the troupe nothing seated a troupe-bound subject")
	}
	p := localDelegating(t, `"read:*"`)
	if err := p.SetTroupe("estate", []byte("[roles.all\n")); err == nil {
		t.Fatal("a broken troupe document loaded")
	}
	if p.HoldsRole(subject(guest)) {
		t.Fatal("a troupe document that does not load seated its subject")
	}
}

// With no policy loaded nobody holds a role; a broken reload keeps the
// policy in force and its holders with it.
func TestHoldsRoleFollowsThePolicyInForce(t *testing.T) {
	missing := NewPolicies(me, facts())
	if err := missing.Reload(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Fatal("a missing policy loaded")
	}
	if missing.HoldsRole(subject(me)) {
		t.Fatal("with no policy loaded the owner holds a role")
	}

	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(narrowRoles), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(me, facts())
	if err := p.Reload(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[[bind]]\nrole = \"owner\"\nprincipals = [\"user:1001\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(path); err == nil {
		t.Fatal("a policy binding an undeclared role loaded")
	}
	if !p.HoldsRole(subject(me)) || p.HoldsRole(subject(other)) {
		t.Fatal("a refused reload changed who holds a role")
	}

	def := NewPolicies(me, facts())
	if err := def.Reload(""); err != nil {
		t.Fatal(err)
	}
	if !def.HoldsRole(subject(me)) || def.HoldsRole(subject(other)) {
		t.Fatal("the default policy seats someone other than its owner")
	}
}

// With no policy at all, the local endpoint and in-process calls pass as they
// always have; any other endpoint passes only the owner, never everyone.
func TestWithNoPolicyOnlyTheOwnerPassesOffTheLocalEndpoint(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	h := Guard(map[string]jkrpc.HandlerFunc{rpc.MethodGC: ok}, AriaHeader{}, LocalOrOwner(me))
	at := func(d Endpoint, p jkauthz.Principal) context.Context {
		return WithEndpoint(WithSubject(context.Background(), subject(p)), d)
	}
	for name, c := range map[string]struct {
		ctx  context.Context
		pass bool
	}{
		"a stranger in process":      {at(EndpointInProcess, other), true},
		"a stranger, door unstamped": {WithSubject(context.Background(), subject(other)), false},
		"a stranger at the local":    {at(EndpointLocal, other), true},
		"the owner at the web door":  {at(EndpointWeb, me), true},
		"a stranger at the web":      {at(EndpointWeb, other), false},
		"a stranger at a node door":  {at(EndpointNode, other), false},
		"a stranger at a new door":   {at(EndpointWeb+1, other), false},
		"nobody at the web door":     {at(EndpointWeb, jkauthz.Principal{}), false},
	} {
		if err := call(t, h, c.ctx, rpc.MethodGC, ""); (err == nil) != c.pass {
			t.Errorf("%s: err = %v, want pass %v", name, err, c.pass)
		}
	}
	if err := call(t, Guard(map[string]jkrpc.HandlerFunc{rpc.MethodGC: ok}, AriaHeader{}, LocalOrOwner(jkauthz.Principal{})), at(EndpointWeb, jkauthz.Principal{}), rpc.MethodGC, ""); err == nil {
		t.Fatal("with no owner known, the zero subject passed the web door")
	}
}

// Only the endpoints whose admission is the policy itself seat by role: the
// local endpoint seats by uid, a node endpoint by troupe membership, and any endpoint
// added later seats by role until it says otherwise.
func TestEveryEndpointButLocalAndNodeSeatsOnlyPolicySubjects(t *testing.T) {
	for d, want := range map[Endpoint]bool{EndpointUnknown: true, EndpointInProcess: false, EndpointLocal: false, EndpointNode: false, EndpointWeb: true, EndpointWeb + 1: true} {
		if got := d.SeatsOnlyPolicySubjects(); got != want {
			t.Errorf("%s: SeatsOnlyPolicySubjects = %v, want %v", d, got, want)
		}
	}
}
