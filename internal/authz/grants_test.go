package authz

// The tests that make grants.go worth having. Every one of them is here
// because authz.Rules answers the same question the opposite way, and the
// whole point of a second policy type is that its zero value denies.

import "testing"

func req(method string, groups ...string) Request {
	return Request{
		Method:   method,
		Identity: Identity{FigaroID: "caller", Authenticated: true, Groups: groups},
	}
}

// THE LOAD-BEARING TEST. An empty table denies. If this ever passes by
// allowing, a config whose grant list failed to parse opens the daemon
// completely -- which is precisely what authz.Rules does and why grants
// could not be built out of it.
func TestZeroGrantsDeniesEverything(t *testing.T) {
	var g Grants
	for _, m := range []string{"figaro.qua", "figaro.set", "figaro.form", "", "*"} {
		if d := g.Check(req(m, "figaro-admin")); d.Allow {
			t.Fatalf("zero Grants allowed %q: the zero value MUST deny", m)
		}
	}
}

func TestGrantsRequiresBothGroupAndMethod(t *testing.T) {
	g := Grants{Table: []Grant{
		{Groups: []string{"readers"}, Methods: []string{"figaro.form"}},
	}}

	if d := g.Check(req("figaro.form", "readers")); !d.Allow {
		t.Fatalf("granted call refused: %s", d.Reason)
	}
	// Right group, wrong method.
	if d := g.Check(req("figaro.qua", "readers")); d.Allow {
		t.Fatal("a group grant leaked to a method it does not name")
	}
	// Right method, wrong group.
	if d := g.Check(req("figaro.form", "writers")); d.Allow {
		t.Fatal("a method grant leaked to a group it does not name")
	}
	// No group at all.
	if d := g.Check(req("figaro.form")); d.Allow {
		t.Fatal("a caller holding no group satisfied a grant")
	}
}

// A grant naming no group is a grant to NOBODY. The tempting reading -- "no
// groups listed means unrestricted" -- turns a half-written config row into
// a wildcard, so the empty case must fail closed like every other.
func TestGrantWithNoGroupsAdmitsNobody(t *testing.T) {
	g := Grants{Table: []Grant{{Methods: []string{"*"}}}}
	if d := g.Check(req("figaro.qua", "figaro-admin")); d.Allow {
		t.Fatal("a grant naming no group admitted a caller")
	}
	if d := g.Check(req("figaro.qua")); d.Allow {
		t.Fatal("a grant naming no group admitted an anonymous caller")
	}
}

func TestMethodGlob(t *testing.T) {
	cases := []struct {
		pattern, method string
		want            bool
	}{
		{"*", "figaro.qua", true},
		{"figaro.qua", "figaro.qua", true},
		{"figaro.qua", "figaro.quack", false}, // exact means exact
		{"figaro.*", "figaro.qua", true},
		{"figaro.*", "figaro.queue.delete", true},
		// A namespace glob does not match the namespace itself, nor a
		// sibling that merely shares a prefix: "figaro.*" must not admit
		// "figaroX.evil".
		{"figaro.*", "figaro", false},
		{"figaro.*", "figaroX.evil", false},
		{"figaro.*", "angelus.configure", false},
		// Case-sensitive: method names are wire constants. A case-folding
		// compare would admit a spelling the table never wrote.
		{"figaro.qua", "FIGARO.QUA", false},
		// No substring or regex semantics, ever.
		{"qua", "figaro.qua", false},
		{".*", "figaro.qua", false},
	}
	for _, c := range cases {
		if got := matchMethod(c.pattern, c.method); got != c.want {
			t.Errorf("matchMethod(%q, %q) = %v, want %v", c.pattern, c.method, got, c.want)
		}
	}
}

// Extra keeps its veto: a table may permit a method, and an invariant may
// still refuse it. Order matters -- the guardrail runs after the grant, not
// instead of it.
func TestGrantsStillConsultsExtraRules(t *testing.T) {
	denyAll := PolicyFunc(func(Request) Decision { return Deny("invariant says no") })
	g := Grants{
		Table: []Grant{{Groups: []string{"admin"}, Methods: []string{"*"}}},
		Extra: denyAll,
	}
	d := g.Check(req("figaro.fork", "admin"))
	if d.Allow {
		t.Fatal("a grant overrode a guardrail rule")
	}
	if d.Reason != "invariant says no" {
		t.Fatalf("reason = %q, want the guardrail's own prose", d.Reason)
	}
}

// A denial has to say enough to act on. A bare "denied" sends the operator
// to the source.
func TestDenialNamesMethodAndCaller(t *testing.T) {
	var g Grants
	d := g.Check(req("figaro.qua", "readers"))
	if d.Allow {
		t.Fatal("expected denial")
	}
	for _, want := range []string{"figaro.qua", "readers"} {
		if !contains(d.Reason, want) {
			t.Errorf("denial %q does not mention %q", d.Reason, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
