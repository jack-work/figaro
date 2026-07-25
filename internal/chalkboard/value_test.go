package chalkboard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mustValue(t testing.TB, text string) Value {
	t.Helper()
	v := NewValue(json.RawMessage(text))
	if !v.IsJSON() {
		t.Fatalf("mustValue: %q is not valid JSON", text)
	}
	return v
}

// canonicalFormPinned locks in the exact canonical encoding. Equality
// semantics for the whole chalkboard hang off encoding/json emitting map keys
// in sorted order; if a toolchain ever changes that, this fails loudly instead
// of silently changing when the agent sees a reminder.
func TestCanonicalFormPinned(t *testing.T) {
	cases := []struct{ in, canon string }{
		{`{ "z" : 1 , "a" : 2 }`, `{"a":2,"z":1}`},
		{`{"b":{"d":1,"c":[3, 4]},"a":null}`, `{"a":null,"b":{"c":[3,4],"d":1}}`},
		{"  \n\t[1,\t2]  ", `[1,2]`},
		{`"x"`, `"x"`},
		{`1.50`, `1.50`},             // json.Number keeps the literal verbatim
		{`{"a":1,"a":2}`, `{"a":2}`}, // duplicate key: last wins (encoding/json)
	}
	for _, tc := range cases {
		got := NewValue(json.RawMessage(tc.in))
		if !got.IsJSON() {
			t.Fatalf("NewValue(%q) not valid JSON", tc.in)
		}
		if string(got.canonical()) != tc.canon {
			t.Errorf("canon(%q) = %q, want %q", tc.in, got.canonical(), tc.canon)
		}
		if string(got.raw) != tc.in {
			t.Errorf("raw(%q) mutated to %q", tc.in, got.raw)
		}
	}
}

func TestValueEqualSemantics(t *testing.T) {
	equal := [][2]string{
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`},
		{`{ "a" : [ 1 , 2 ] }`, `{"a":[1,2]}`},
		{`{"a":{"x":1,"y":2}}`, `{"a":{"y":2,"x":1}}`},
		{`"<b>"`, `"\u003cb\u003e"`}, // escape spelling is not semantic
		{`null`, `  null `},
		{`{"a":1,"a":2}`, `{"a":2}`},
		{`{}`, `{ }`},
	}
	for _, pair := range equal {
		a, b := NewValue(json.RawMessage(pair[0])), NewValue(json.RawMessage(pair[1]))
		if !a.Equal(b) || !b.Equal(a) {
			t.Errorf("%s != %s, want equal", pair[0], pair[1])
		}
	}

	unequal := [][2]string{
		{`1`, `1.0`},       // documented: numbers compare by literal token
		{`100`, `1e2`},     // ditto
		{`[1,2]`, `[2,1]`}, // arrays are ordered
		{`{"a":1}`, `{"a":"1"}`},
		{`null`, `0`},
		{`{"a":1}`, `{"a":1,"b":2}`},
	}
	for _, pair := range unequal {
		a, b := NewValue(json.RawMessage(pair[0])), NewValue(json.RawMessage(pair[1]))
		if a.Equal(b) || b.Equal(a) {
			t.Errorf("%s == %s, want unequal", pair[0], pair[1])
		}
	}
}

func TestValueRawPreservedAndEmitted(t *testing.T) {
	const in = `{ "z":  1,
  "a": 2 }`
	v := NewValue(json.RawMessage(in))
	if v.String() != in {
		t.Fatalf("String() = %q, want the exact input", v.String())
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	// json.Marshal compacts a RawMessage but must not reorder or rewrite it.
	if !bytes.Contains(out, []byte(`"z":1`)) || bytes.Index(out, []byte(`"z"`)) > bytes.Index(out, []byte(`"a"`)) {
		t.Fatalf("MarshalJSON emitted canonical form: %s", out)
	}
	// The canonical form is emphatically not what comes out.
	if string(v.canonical()) == v.String() {
		t.Fatal("test is vacuous: raw and canon coincide")
	}
}

func TestValueInvalidJSONFallsBackToBytes(t *testing.T) {
	bad := []string{`{`, `not json`, `1 2`, `{"a":1} trailing`, "\x00\xff"}
	for _, s := range bad {
		v := NewValue(json.RawMessage(s))
		if v.IsJSON() {
			t.Errorf("NewValue(%q) reported valid JSON", s)
		}
		if !v.Equal(NewValue(json.RawMessage(s))) {
			t.Errorf("NewValue(%q) not equal to itself", s)
		}
		if v.Equal(NewValue(json.RawMessage(s + "x"))) {
			t.Errorf("NewValue(%q) equal to a different byte string", s)
		}
		// An invalid value never equals a valid one.
		if v.Equal(mustValue(t, `{"a":1}`)) {
			t.Errorf("NewValue(%q) equal to a valid value", s)
		}
		if got := v.String(); got != s {
			t.Errorf("String() = %q, want %q", got, s)
		}
	}
}

func TestValueEmptyIsNull(t *testing.T) {
	v := NewValue(nil)
	if !v.IsJSON() || v.String() != "null" {
		t.Fatalf("NewValue(nil) = %q valid=%v, want the null literal", v, v.IsJSON())
	}
	if !v.Equal(mustValue(t, `null`)) || !v.Equal(NewValue(json.RawMessage(""))) {
		t.Fatal("empty input did not normalise to null")
	}
}

func TestValueZeroIsNull(t *testing.T) {
	var v Value
	if v.String() != "null" {
		t.Fatalf("zero Value String() = %q", v.String())
	}
	out, err := json.Marshal(v)
	if err != nil || string(out) != "null" {
		t.Fatalf("zero Value marshalled to %q (%v)", out, err)
	}
	if v.IsJSON() {
		t.Fatal("zero Value claims to carry parsed JSON")
	}
	// Zero equals zero, and (because its raw is empty, not "null") is not
	// claimed to equal an explicit null literal.
	if !v.Equal(Value{}) {
		t.Fatal("zero Value not equal to itself")
	}
	if v.Equal(mustValue(t, `null`)) {
		t.Fatal("zero Value equals an explicit null; document this if intended")
	}
}

func TestValueDeepNestingDoesNotPanic(t *testing.T) {
	for _, depth := range []int{10, 100, 5_000, 20_000, 200_000} {
		in := strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth)
		v := NewValue(json.RawMessage(in)) // must not panic or blow the stack
		if v.String() != in {
			t.Fatalf("depth %d: raw not preserved", depth)
		}
		if v.IsJSON() {
			// Shallow enough to parse: equality must still work.
			if !v.Equal(NewValue(json.RawMessage(in))) {
				t.Fatalf("depth %d: not equal to itself", depth)
			}
		} else if depth < 1_000 {
			t.Fatalf("depth %d unexpectedly rejected", depth)
		}
	}
}

func TestValueRoundTripThroughUnmarshal(t *testing.T) {
	const doc = `{"k":{ "b":1, "a":2 }}`
	var m map[string]Value
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	if got := m["k"].String(); got != `{ "b":1, "a":2 }` {
		t.Fatalf("UnmarshalJSON did not preserve bytes: %q", got)
	}
	if !m["k"].Equal(mustValue(t, `{"a":2,"b":1}`)) {
		t.Fatal("unmarshalled value lost canonical equality")
	}
}

func TestValueDecodeAndEncode(t *testing.T) {
	v, err := EncodeValue(map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]int
	if err := v.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if back["a"] != 1 {
		t.Fatalf("round trip lost data: %v", back)
	}
	if err := NewValue(json.RawMessage(`{`)).Decode(&back); err == nil {
		t.Fatal("Decode accepted invalid JSON")
	}
}

// TestValueMarshalsIdenticallyToRawMessage is the ruling-1 pin: swapping
// json.RawMessage for Value must not move a single byte of output. Whatever
// encoding/json does to an embedded RawMessage today -- it compacts it and
// escapes <, > and & -- it must do to a Value, and nothing more: nested object
// key order preserved, \u00e9 not re-escaped, literal é left alone, number
// spelling untouched.
func TestValueMarshalsIdenticallyToRawMessage(t *testing.T) {
	inputs := []string{
		`{"z":1,"a":2}`,
		"{\"z\": 1,\n  \"a\": [2, {\"d\":4, \"c\":3}] }",
		`{"html":"<b>&</b>"}`,
		`{"esc":"\u00e9","lit":"é"}`,
		`{"nums":[1.0,1,1e2,-0,123456789012345678901234567890]}`,
		`"\ud83d\ude00"`,
		`{"deep":{"b":{"y":1,"x":2},"a":null}}`,
		`[]`,
		`null`,
	}
	for _, in := range inputs {
		raw := json.RawMessage(in)
		want, err := json.Marshal(map[string]json.RawMessage{"k": raw})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got, err := json.Marshal(map[string]Value{"k": NewValue(raw)})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Value diverged from json.RawMessage:\n in   %s\n got  %s\n want %s", in, got, want)
		}
		// And the canonical form is emphatically NOT what came out.
		v := NewValue(raw)
		if bytes.Contains(got, v.canonical()) != bytes.Contains(want, v.canonical()) {
			t.Errorf("%s: canonical form leaked into output", in)
		}
	}
}

// Reordering an object's keys must not change what is emitted -- only whether
// Equal fires. This is the whole raw/canon split in four lines.
func TestValueReorderedKeysEmitTheirOwnBytes(t *testing.T) {
	a := NewValue(json.RawMessage(`{"a":1,"b":2}`))
	b := NewValue(json.RawMessage(`{"b":2,"a":1}`))
	if !a.Equal(b) {
		t.Fatal("reordered keys should compare equal")
	}
	if a.String() == b.String() {
		t.Fatal("test is vacuous")
	}
	if a.String() != `{"a":1,"b":2}` || b.String() != `{"b":2,"a":1}` {
		t.Fatalf("bytes were rewritten: %s / %s", a, b)
	}
}
