package authz

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

var guest = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "ana@auth.kelliher.info"}

const everything = `
[roles.all]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "all"
principals = ["user:ana@auth.kelliher.info"]
`

func localDelegating(t *testing.T, delegated string) *Policies {
	t.Helper()
	return loaded(t, `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[[bind]]
role = "owner"
principals = ["user:1000"]

[roles.lent]
grants = [`+delegated+`]

[[bind]]
role = "lent"
principals = ["troupe:estate"]
`)
}

func withTroupe(t *testing.T, p *Policies, body string) *Policies {
	t.Helper()
	if err := p.SetTroupe("estate", []byte(body)); err != nil {
		t.Fatal(err)
	}
	return p
}

// A troupe grant counts on a node only as far as that node's own policy
// lends the same right to the troupe.
func TestATroupeGrantAppliesOnlyAsFarAsTheNodeDelegates(t *testing.T) {
	p := withTroupe(t, localDelegating(t, `{ verbs = ["read", "listen"], on = ["*"] }`), everything)
	if d := p.Allow(subject(guest), rpc.VerbRead, Aria("aaaa0002")); !d.Allowed {
		t.Fatalf("delegated read: %s", d.Reason())
	}
	d := p.Allow(subject(guest), rpc.VerbSend, Aria("aaaa0002"))
	if d.Allowed {
		t.Fatal("send was not delegated, yet the troupe granted it here")
	}
	if !strings.Contains(d.Reason(), "troupe estate grants it, but this node does not delegate it") {
		t.Fatalf("the refusal does not name the layer: %s", d.Reason())
	}
}

func TestWithoutATroupePolicyOrDelegationTheLocalPolicyDecidesAsBefore(t *testing.T) {
	p := localDelegating(t, `{ verbs = ["*"], on = ["*"] }`)
	if d := p.Allow(subject(guest), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Fatal("no troupe policy, yet a stranger reads")
	}
	if d := p.Allow(subject(me), rpc.VerbKill, Aria("aaaa0002")); !d.Allowed {
		t.Fatal(d.Reason())
	}
	if err := p.SetTroupe("estate", nil); err != nil {
		t.Fatal(err)
	}
	if d := p.Allow(subject(guest), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Fatal("a troupe with no document granted")
	}
	none := withTroupe(t, loaded(t, sandboxPolicy), everything)
	if d := none.Allow(subject(guest), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Fatal("a node that delegates nothing honoured a troupe grant")
	}
}

// Even a document granting every verb on everything, on a node that lends
// the troupe everything, never reaches the two local-only rights: admin on
// the troupe (changing its policy, revoking, leaving) and sandbox:local.
func TestAnAllGrantingTroupeDocumentCannotReachTheLocalOnlyRights(t *testing.T) {
	p := withTroupe(t, localDelegating(t, `{ verbs = ["*"], on = ["*"] }`), everything)
	for _, c := range []struct {
		verb rpc.Verb
		r    Resource
		want bool
	}{
		{rpc.VerbAdmin, Troupe("estate"), false},
		{rpc.VerbUse, Sandbox("local"), false},
		{rpc.VerbUse, Sandbox(""), false},
		{rpc.VerbJoin, Troupe("estate"), true},
		{rpc.VerbSend, Aria("aaaa0002"), true},
		{rpc.VerbUse, Sandbox("dev-3"), true},
	} {
		if got := p.Allow(subject(guest), c.verb, c.r).Allowed; got != c.want {
			t.Errorf("%s on %v: %v", c.verb, c.r, got)
		}
	}
	if d := p.Allow(subject(me), rpc.VerbAdmin, Troupe("estate")); !d.Allowed {
		t.Fatalf("the local owner lost troupe admin: %s", d.Reason())
	}
}

func TestATroupeDocumentThatDoesNotCompileGrantsNothing(t *testing.T) {
	p := localDelegating(t, `{ verbs = ["*"], on = ["*"] }`)
	if err := p.SetTroupe("estate", []byte("[roles.x]\ngrants = [{ verbs = [\"fly\"], on = [\"*\"] }]\n")); err == nil {
		t.Fatal("an unknown verb compiled")
	}
	if d := p.Allow(subject(guest), rpc.VerbRead, Aria("aaaa0002")); d.Allowed {
		t.Fatal("a broken troupe document granted")
	}
}

func TestSettingATroupeDocumentRefusesTheLocalOnlyGrants(t *testing.T) {
	ok := []string{
		everything,
		"[roles.j]\ngrants = [{ verbs = [\"join\"], on = [\"troupe:estate\"] }]\n",
		"[roles.s]\ngrants = [{ verbs = [\"use\"], on = [\"sandbox:dev-3\"] }]\n",
		everything + "\n[[issuers]]\nissuer = \"https://id.example.org\"\njwks = \"https://id.example.org/jwks\"\nclients = { x = \"service\" }\n",
	}
	for _, body := range ok {
		if err := CheckTroupeDocument([]byte(body), "estate", nil); err != nil {
			t.Errorf("refused: %v\n%s", err, body)
		}
	}
	bad := map[string]string{
		"use on sandbox:local":      "[roles.s]\ngrants = [{ verbs = [\"use\"], on = [\"sandbox:local\"] }]\n",
		"admin on the troupe":       "[roles.a]\ngrants = [{ verbs = [\"admin\"], on = [\"troupe:estate\"] }]\n",
		"every verb on the troupe":  "[roles.a]\ngrants = [{ verbs = [\"*\"], on = [\"troupe:estate\"] }]\n",
		"binding the troupe itself": everything + "\n[[bind]]\nrole = \"all\"\nprincipals = [\"troupe:estate\"]\n",
		"an http issuer":            everything + "\n[[issuers]]\nissuer = \"http://id.example.org\"\njwks = \"http://j\"\nclients = { x = \"service\" }\n",
		"an unknown verb":           "[roles.x]\ngrants = [{ verbs = [\"fly\"], on = [\"*\"] }]\n",
	}
	for _, kind := range []string{"node", "sandbox", "troupe", "robot"} {
		bad["a client resolved to "+kind] = everything + "\n[[issuers]]\nissuer = \"https://id.example.org\"\njwks = \"https://id.example.org/jwks\"\nclients = { a = \"user\", x = \"" + kind + "\" }\n"
	}
	for name, body := range bad {
		if err := CheckTroupeDocument([]byte(body), "estate", nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheIssuersOfATroupeDocumentAreReadApart(t *testing.T) {
	doc, err := ParseTroupeDocument([]byte(everything + "\n[[issuers]]\nissuer = \"https://id.example.org\"\njwks = \"https://id.example.org/jwks\"\nusers = [\"figaro-cli\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Issuers) != 1 || doc.Issuers[0].Issuer != "https://id.example.org" || doc.Issuers[0].Users[0] != "figaro-cli" {
		t.Fatalf("%+v", doc.Issuers)
	}
}

// The troupe is a kind only a node's own policy names; a caller mapped to
// it would hold everything the node lends the troupe, directly.
func TestNoCallerMayBeTheTroupe(t *testing.T) {
	if _, err := NewPeercred(1000, config.PeercredConfig{UIDs: map[string]string{"1001": "troupe:estate"}}); err == nil {
		t.Error("a uid mapped to the troupe")
	}
	if _, err := NewIssuers([]config.IssuerConfig{{Issuer: "https://a", JWKS: "http://j", Clients: map[string]string{"x": "troupe"}}}, nil, nil); err == nil {
		t.Error("a client resolved to the troupe")
	}
	if _, err := NewUpstream(config.UpstreamConfig{Issuer: kelliher, Services: map[string]string{"x": "troupe"}}, webDoor, "", nil); err == nil {
		t.Error("an edge service resolved to the troupe")
	}
}

// jkauthz's kind wildcard ignores the issuer scope in the id, so the kinds a
// troupe issuer may produce are the only fence between its callers and a
// node's own `node:*`, `sandbox:*` bindings.
func TestAKindWildcardMatchesEveryIssuersScope(t *testing.T) {
	pol, err := jkauthz.Compile([]byte(`
[roles.r]
grants = [{ verbs = ["read"], on = ["*"] }]

[[bind]]
role = "r"
principals = ["node:*", "user:*", "service:*", "sandbox:*"]
`), Schema(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"node:x@auth.example.org",
		"node:b81f0e2a",
		"user:jack@auth.kelliher.info",
		"user:ana@id.example.org/realms/guests",
		"user:1000",
		"service:ci@troupe:estate",
		"sandbox:figbox@auth.kelliher.info",
	} {
		p, err := jkauthz.ParsePrincipal(name)
		if err != nil {
			t.Fatal(err)
		}
		if !pol.Allow(jkauthz.Subject{Principal: p}, string(rpc.VerbRead), Daemon).Allowed {
			t.Errorf("%s not matched by its kind's wildcard", name)
		}
	}
	for _, name := range []string{"spain/node:x", "troupe:estate"} {
		p, _ := jkauthz.ParsePrincipal(name)
		if pol.Allow(jkauthz.Subject{Principal: p}, string(rpc.VerbRead), Daemon).Allowed {
			t.Errorf("%s matched a wildcard of no node or another kind", name)
		}
	}
}
