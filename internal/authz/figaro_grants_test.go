package authz

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
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
	want := "read:*, listen:aria:x, send:aria:x, fork:aria:x, set:aria:x, create:aria:x, kill:aria:x, admin:aria:x, use:aria:x, join:aria:x, egress:*"
	if strings.Join(got, ", ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, ", "), want)
	}
	if out := ExportGrants(nil); out == nil || len(out) != 0 {
		t.Fatalf("an empty set exports %#v, want []", out)
	}
}

// A successor is born outside its predecessor's lineage, so the daemon writes
// the predecessor's effective set into the successor's own key. Inspector XV:
// an object naming exactly the predecessor goes back to its self form, and
// nothing else moves.
func TestASuccessorsScopesAreWrittenOnlyWhenAncestorsNarrow(t *testing.T) {
	for _, c := range []struct {
		name     string
		own      map[string]string
		figaro   string
		want     string
		narrowed bool
	}{
		{"its own lineage under a narrowing parent", map[string]string{"top": `["send:*"]`, "kid": `["*:lineage:self"]`}, "kid", "send:lineage:self", true},
		{"itself alone", map[string]string{"top": `["send:*"]`, "kid": `["*:aria:self"]`}, "kid", "send:aria:self", true},
		{"an ancestor's lineage stays literal", map[string]string{"kid": `["send:lineage:top"]`}, "grandkid", "send:lineage:top", true},
		{"an ancestor's own self stays literal", map[string]string{"top": `["send:lineage:self"]`}, "kid", "send:lineage:top", true},
		{"a longer id is not the predecessor", map[string]string{"top": `["send:aria:kidney", "read:lineage:kid"]`}, "kid", "read:lineage:self, send:aria:kidney", true},
		{"only its own key: nothing to write", map[string]string{"kid": `["read:*"]`}, "kid", "", false},
		{"no key anywhere: nothing to write", nil, "grandkid", "", false},
	} {
		got, narrowed, held := NewPolicies(me, lineageFakes(c.own)).SuccessorScopes(c.figaro)
		if !held || narrowed != c.narrowed || got.String() != c.want {
			t.Errorf("%s: %q narrowed=%v held=%v, want %q narrowed=%v", c.name, got, narrowed, held, c.want, c.narrowed)
		}
	}
	if _, _, held := NewPolicies(me, lineageFakes(nil)).SuccessorScopes("elsewhere"); held {
		t.Error("a figaro this node does not hold has scopes to hand on")
	}
}

// Every verb, over the predecessor, its child, an ancestor and a stranger,
// with the predecessor and its child mapped to the successor and its child.
func TestASuccessorReachesNothingItsPredecessorCouldNot(t *testing.T) {
	own := map[string]string{
		"top": `["send:*", "fork:*", "set:*", "read:lineage:self"]`,
		"kid": `["send:lineage:self", "fork:aria:self", "set:*", "read:*", "kill:*"]`,
	}
	f := lineageFakes(own)
	scopes, narrowed, held := NewPolicies(me, f).SuccessorScopes("kid")
	if !held || !narrowed {
		t.Fatalf("narrowed=%v held=%v", narrowed, held)
	}
	written, _ := json.Marshal(scopes.Strings())
	f.own["heir"] = string(written)
	f.chains["heir"], f.chains["heirkid"] = []string{"heir"}, []string{"heir", "heirkid"}
	f.parents["heirkid"] = "heir"
	p := NewPolicies(me, f)
	if err := p.load([]byte(sandboxPolicy)); err != nil {
		t.Fatal(err)
	}
	may := func(figaro string, verb rpc.Verb, on string) bool {
		return p.AllowFigaro(subject(me), figaro, verb, Aria(on)).Allowed
	}
	if !may("kid", rpc.VerbSend, "grandkid") || may("kid", rpc.VerbKill, "kid") || !may("kid", rpc.VerbRead, "top") {
		t.Fatal("the predecessor's own table is not the one this test assumes")
	}

	// The predecessor read itself and its child as members of top's lineage.
	// That grant stays literal, and the successor is outside that lineage.
	lostWithTheLineage := map[string]bool{"read heir": true, "read heirkid": true}
	predecessors := map[string]string{"heir": "kid", "heirkid": "grandkid"}
	for _, verb := range rpc.Verbs() {
		for _, pair := range [][2]string{{"kid", "heir"}, {"grandkid", "heirkid"}, {"top", "top"}, {"aaaa0001", "aaaa0001"}} {
			was, is := may("kid", verb, pair[0]), may("heir", verb, pair[1])
			if lostWithTheLineage[string(verb)+" "+pair[1]] {
				if !was || is {
					t.Errorf("%s on %s: predecessor %v, successor %v, want true then false", verb, pair[1], was, is)
				}
				continue
			}
			if was != is {
				t.Errorf("%s: predecessor on %s %v, successor on %s %v", verb, pair[0], was, pair[1], is)
			}
		}
		for _, on := range []string{"kid", "grandkid", "top", "aaaa0001", "heir", "heirkid"} {
			before := on
			if mapped, ok := predecessors[on]; ok {
				before = mapped
			}
			if may("heir", verb, on) && !may("kid", verb, before) {
				t.Errorf("the successor may %s on %s; its predecessor could not on %s", verb, on, before)
			}
		}
	}
}
