package outfit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/outfit"
)

// One parser for every surface: names, key=value sugar, JSON literals, and any
// mix of them. The sugar is exactly the literal it stands for.
func TestParseSpec(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // canonical rendering
		err  bool
	}{
		{in: "sonn5", want: "sonn5"},
		{in: "a,b", want: "a,b"},
		{in: " a , b ", want: "a,b"},
		{in: "a,,b", want: "a,b"},
		{in: "", want: ""},
		{in: `ttl=1h`, want: `{"ttl":"1h"}`},
		{in: `ttl="1h"`, want: `{"ttl":"1h"}`},
		{in: `n=3`, want: `{"n":3}`},
		{in: `on=true`, want: `{"on":true}`},
		{in: `mantra="cool, thing"`, want: `{"mantra":"cool, thing"}`},
		{in: `sonn5,ttl=1h`, want: `sonn5,{"ttl":"1h"}`},
		{in: `{"ttl":"1h","n":3}`, want: `{"n":3,"ttl":"1h"}`},
		{in: `{"a":1},b`, want: `{"a":1},b`},
		{in: `system.model=x`, want: `{"system.model":"x"}`},
		{in: `{"layers":["a","b"]}`, want: `{"layers":["a","b"]}`},
		{in: "a/b", err: true},
		{in: "../secrets", err: true},
		{in: "{not json}", err: true},
		{in: "=v", err: true},
	} {
		got, err := outfit.ParseSpec(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseSpec(%q): want error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseSpec(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// The sugar and the literal must be the same value on the wire, and the wire
// must survive a round trip. The legacy scalar form still means one name.
func TestSpecWire(t *testing.T) {
	sugar, err := outfit.ParseSpec(`a,ttl=1h`)
	if err != nil {
		t.Fatal(err)
	}
	literal, err := outfit.ParseSpec(`a,{"ttl":"1h"}`)
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := json.Marshal(sugar)
	lb, _ := json.Marshal(literal)
	if string(sb) != string(lb) {
		t.Errorf("sugar %s != literal %s", sb, lb)
	}
	if want := `["a",{"ttl":"1h"}]`; string(sb) != want {
		t.Errorf("wire = %s, want %s", sb, want)
	}

	var back outfit.Spec
	if err := json.Unmarshal(sb, &back); err != nil {
		t.Fatal(err)
	}
	if back.String() != sugar.String() {
		t.Errorf("round trip: %s != %s", back, sugar)
	}

	var scalar outfit.Spec
	if err := json.Unmarshal([]byte(`"sonn5"`), &scalar); err != nil {
		t.Fatal(err)
	}
	if scalar.String() != "sonn5" || scalar.Label() != "sonn5" {
		t.Errorf("scalar form: %s / %s", scalar, scalar.Label())
	}

	var null outfit.Spec
	if err := json.Unmarshal([]byte(`null`), &null); err != nil || !null.IsEmpty() {
		t.Errorf("null: %v %v", null, err)
	}
}

// An aria born on a spec is stamped with a label a listing can show; inline
// terms have no name of their own.
func TestSpecLabel(t *testing.T) {
	for in, want := range map[string]string{
		"a":               "a",
		"a,b":             "a,b",
		`a,ttl=1h`:        "a,{}",
		`{"ttl":"1h"}`:    "{}",
		`{"a":1},{"b":2}`: "{},{}",
	} {
		spec, err := outfit.ParseSpec(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := spec.Label(); got != want {
			t.Errorf("ParseSpec(%q).Label() = %q, want %q", in, got, want)
		}
	}
}

// An inline term is argv, and one fold becomes one chalkboard record: a record
// larger than a WAL segment cannot be appended at all, so the boundary refuses
// it rather than the store losing it.
func TestSpecRejectsOversizedInline(t *testing.T) {
	big := `{"k":"` + strings.Repeat("x", outfit.MaxInlineBytes) + `"}`
	if _, err := outfit.ParseSpec(big); err == nil {
		t.Fatal("accepted an oversized inline term")
	}
	var wire outfit.Spec
	if err := json.Unmarshal([]byte("["+big+"]"), &wire); err != nil {
		t.Fatal(err)
	}
	if err := wire.Validate(); err == nil {
		t.Fatal("wire form skipped the limit")
	}
}

// A name is a file basename. Everything the spec grammar itself uses must be
// refused there, or a malformed literal arrives as a name that can only ever
// be missing.
func TestNameGrammarIsNarrow(t *testing.T) {
	for _, bad := range []string{"a b", "a\tb", "[1,2]", "a}", `a"b`, "{oops", "a\u00a0b", "a\x00b", ".", "..", "a/b", `a\b`} {
		if _, err := outfit.ParseSpec(bad); err == nil {
			t.Errorf("ParseSpec(%q) accepted it as a name", bad)
		}
	}
	for _, good := range []string{"sonn5", "pr-review", "opus5.1", "a_b", "modèle"} {
		if _, err := outfit.ParseSpec(good); err != nil {
			t.Errorf("ParseSpec(%q): %v", good, err)
		}
	}
}

// The stamp an inline-born aria carries must not read back as a spec: that is
// what lets a listing say "this version cannot be re-resolved" truthfully.
func TestInlineLabelIsNotAName(t *testing.T) {
	spec, err := outfit.ParseSpec(`base,ttl=1h`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outfit.ParseSpec(spec.Label()); err == nil {
		t.Fatalf("label %q parsed back as a spec", spec.Label())
	}
	names, err := outfit.ParseSpec(outfit.Names("a", "b").Label())
	if err != nil || names.String() != "a,b" {
		t.Fatalf("an all-names label must round trip: %v %v", names, err)
	}
}

// Structure is balanced or it is an error. Unbalanced brackets and quotes used
// to switch the splitter into a mode where commas stopped separating, and the
// whole tail arrived as one impossible "name".
func TestSpecStructureMustBalance(t *testing.T) {
	for _, bad := range []string{`a},b`, `a],b`, `{"a":1}}`, `k="unclosed`, `"a,b"`, `{"a":1`, `-j`, `--outfit`, `{}`} {
		if got, err := outfit.ParseSpec(bad); err == nil {
			t.Errorf("ParseSpec(%q) = %s, want an error", bad, got)
		}
	}
}

// The shell is half of this grammar. `-O {mantra:test}` is not JSON, and
// `-O {a:1,b:2}` never arrives at all — bash expands it into two words with
// the braces gone — so both must fail in the vocabulary of the fix.
func TestUnquotedLiteralsAreExplained(t *testing.T) {
	_, err := outfit.ParseSpec("{mantra:test}")
	if err == nil || !strings.Contains(err.Error(), "mantra=test") {
		t.Errorf("want the sugar suggested, got %v", err)
	}
	// What the shell actually delivers once it has expanded the braces.
	_, err = outfit.ParseSpec("mantra:test")
	if err == nil || !strings.Contains(err.Error(), "cannot contain `:`") {
		t.Errorf("want the brace-expansion trap named, got %v", err)
	}
}
