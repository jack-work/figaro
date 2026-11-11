package authz

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/jkauthz"
)

type figaroFakes struct {
	fakeFacts
	chains map[string][]string
	own    map[string]string
	broken map[string]bool
}

func (f figaroFakes) Ancestry(id string) ([]string, bool) {
	c, ok := f.chains[id]
	return c, ok
}

func (f figaroFakes) OwnGrants(id string) (json.RawMessage, bool, error) {
	if f.broken[id] {
		return nil, false, errors.New("unreadable")
	}
	raw, ok := f.own[id]
	return json.RawMessage(raw), ok, nil
}

func lineageFakes(own map[string]string) figaroFakes {
	f := facts()
	f.parents = map[string]string{"kid": "top", "grandkid": "kid"}
	return figaroFakes{
		fakeFacts: f,
		chains:    map[string][]string{"top": {"top"}, "kid": {"top", "kid"}, "grandkid": {"top", "kid", "grandkid"}},
		own:       own,
	}
}

func effectiveOf(t *testing.T, f figaroFakes, id string) string {
	t.Helper()
	p := NewPolicies(me, f)
	eff, held := p.Effective(id)
	if !held {
		t.Fatalf("%s not held", id)
	}
	return eff.String()
}

func TestNoGrantsAnywhereHoldsEveryFigaroVerbAndNoForeignOne(t *testing.T) {
	f := lineageFakes(nil)
	for _, id := range []string{"top", "kid", "grandkid"} {
		if got := effectiveOf(t, f, id); got != "*:*" {
			t.Errorf("%s: %s", id, got)
		}
	}
	p := NewPolicies(me, f)
	eff, _ := p.Effective("grandkid")
	for _, g := range []jkauthz.Grant{{Verb: "egress", Object: "*"}, {Verb: "cred", Object: "github"}, {Verb: "egress", Object: "host:443"}} {
		if (selectors{}).covered(eff, g) {
			t.Errorf("a figaro with no grants holds %s", g)
		}
	}
}

func TestForeignGrantsComeOnlyFromWhatIsWrittenAndNarrowDownTheTree(t *testing.T) {
	f := lineageFakes(map[string]string{
		"top": `["*:*", "egress:*", "cred:github"]`,
		"kid": `["read:*", "send:*", "egress:api.example.com:443", "cred:gitlab"]`,
	})
	if got := effectiveOf(t, f, "top"); got != "*:*, cred:github, egress:*" {
		t.Errorf("top: %s", got)
	}
	if got := effectiveOf(t, f, "kid"); got != "read:*, send:*, egress:api.example.com:443" {
		t.Errorf("kid: %s", got)
	}
	if got := effectiveOf(t, f, "grandkid"); got != "read:*, send:*" {
		t.Errorf("an absent set keeps the figaro verbs and drops the foreign ones: %s", got)
	}
}

func TestIntersectionKeepsTheNarrowerSide(t *testing.T) {
	m := selectors{facts: lineageFakes(nil).fakeFacts}
	for _, c := range []struct{ a, b, want string }{
		{`["read:*"]`, `["read:aria:x"]`, "read:aria:x"},
		{`["*:aria:x"]`, `["read:*"]`, "read:aria:x"},
		{`["*:*"]`, `["kill:lineage:top"]`, "kill:lineage:top"},
		{`["read:lineage:top"]`, `["read:aria:kid"]`, "read:aria:kid"},
		{`["read:lineage:top"]`, `["read:lineage:grandkid"]`, "read:lineage:grandkid"},
		{`["read:lineage:kid"]`, `["read:aria:top"]`, ""},
		{`["read:*"]`, `["send:*"]`, ""},
		{`["*:*"]`, `["egress:*"]`, ""},
		{`["egress:*"]`, `["egress:host:443"]`, "egress:host:443"},
		{`["read:aria:a", "read:aria:b"]`, `["read:aria:b", "read:aria:c"]`, "read:aria:b"},
	} {
		a, err := DecodeFigaroGrants(json.RawMessage(c.a))
		if err != nil {
			t.Fatal(err)
		}
		b, err := DecodeFigaroGrants(json.RawMessage(c.b))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.intersect(a, b).String(); got != c.want {
			t.Errorf("%s ∩ %s = %q, want %q", c.a, c.b, got, c.want)
		}
		if got := m.intersect(b, a).String(); got != c.want {
			t.Errorf("%s ∩ %s = %q, want %q", c.b, c.a, got, c.want)
		}
	}
}

func TestSelfMeansTheFigaroAtEachLevel(t *testing.T) {
	f := lineageFakes(map[string]string{
		"top": `["*:lineage:self", "read:*"]`,
		"kid": `["kill:aria:self", "read:*"]`,
	})
	if got := effectiveOf(t, f, "top"); got != "*:lineage:top, read:*" {
		t.Errorf("top: %s", got)
	}
	if got := effectiveOf(t, f, "kid"); got != "read:*, kill:aria:kid" {
		t.Errorf("kid: %s", got)
	}
}

func TestAnUnreadableOrMalformedSetHoldsNothing(t *testing.T) {
	f := lineageFakes(map[string]string{"top": `["read:*"]`, "kid": `["read"]`})
	if got := effectiveOf(t, f, "kid"); got != "" {
		t.Errorf("malformed: %s", got)
	}
	if got := effectiveOf(t, f, "grandkid"); got != "" {
		t.Errorf("below a malformed set: %s", got)
	}
	f = lineageFakes(nil)
	f.broken = map[string]bool{"top": true}
	if got := effectiveOf(t, f, "kid"); got != "" {
		t.Errorf("below an unreadable form: %s", got)
	}
}

func TestAFigaroThisNodeDoesNotHoldHasNoEffectiveSet(t *testing.T) {
	p := NewPolicies(me, lineageFakes(nil))
	if _, held := p.Effective("elsewhere"); held {
		t.Fatal("held")
	}
	if _, held := NewPolicies(me, facts()).Effective("top"); held {
		t.Fatal("facts without lineage still answered")
	}
}

func TestFigaroGrantsAreValidatedOnlyWhereFigaroOwnsTheVerb(t *testing.T) {
	for _, bad := range []string{`["read"]`, `["read:aria:*"]`, `["read:moon:1"]`, `["*:egress.example.com"]`, `["fly:"]`, `"read:*"`, `null`} {
		if _, err := DecodeFigaroGrants(json.RawMessage(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	gs, err := DecodeFigaroGrants(json.RawMessage(`["egress:host:443", "kill:*", "read:aria:x", "*:*", "cred:aria:*"]`))
	if err != nil {
		t.Fatal(err)
	}
	if gs.String() != "*:*, cred:aria:*, egress:host:443" {
		t.Fatal(gs.String())
	}
}

func TestAWriterHoldsWhatItsEffectiveSetCovers(t *testing.T) {
	f := lineageFakes(map[string]string{"top": `["read:*", "kill:lineage:self", "egress:*"]`})
	p := NewPolicies(me, f)
	for want, ok := range map[string]bool{
		`["read:aria:x"]`:         true,
		`["kill:aria:self"]`:      true,
		`["kill:lineage:self"]`:   true,
		`["egress:host:443"]`:     true,
		`["send:*"]`:              false,
		`["cred:github"]`:         false,
		`["kill:aria:elsewhere"]`: false,
		`["read:*", "egress:*", "kill:lineage:grandkid"]`: true,
	} {
		gs, err := DecodeFigaroGrants(json.RawMessage(want))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.HoldsGrants("top", gs, "kid"); (err == nil) != ok {
			t.Errorf("%s: %v", want, err)
		}
	}
}

func TestAStarVerbCoversOnlyFigarosVerbs(t *testing.T) {
	m := selectors{}
	star := jkauthz.Grants{{Verb: "*", Object: "*"}}
	for _, g := range []jkauthz.Grant{{Verb: "egress", Object: "*"}, {Verb: "cred", Object: "github"}, {Verb: "egress", Object: "host:443"}} {
		if m.covered(star, g) {
			t.Errorf("*:* covers %s", g)
		}
	}
	if !m.covered(star, jkauthz.Grant{Verb: "kill", Object: "aria:x"}) {
		t.Error("*:* does not cover a figaro verb")
	}
}

func TestExportedSetsSpellEveryStarVerbOut(t *testing.T) {
	gs, err := DecodeFigaroGrants(json.RawMessage(`["*:aria:x", "read:*", "egress:*"]`))
	if err != nil {
		t.Fatal(err)
	}
	got := ExportGrants(gs)
	want := "read:*, listen:aria:x, send:aria:x, fork:aria:x, set:aria:x, create:aria:x, kill:aria:x, admin:aria:x, use:aria:x, join:aria:x, adopt:aria:x, egress:*"
	if strings.Join(got, ", ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, ", "), want)
	}
	if out := ExportGrants(nil); out == nil || len(out) != 0 {
		t.Fatalf("an empty set exports %#v, want []", out)
	}
}
