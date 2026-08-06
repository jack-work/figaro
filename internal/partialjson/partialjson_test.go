package partialjson

import (
	"encoding/json"
	"testing"
)

var fixtures = []struct {
	name  string
	json  string
	field string
}{
	{"escapes", `{"content":"line1\nline2\ttab\"quote\\back\/slash\r\b\f"}`, "content"},
	{"uXXXX", `{"content":"A\u0041 mid\u00e9 end"}`, "content"},
	{"after-others", `{"path":"/f","n":123,"flag":true,"content":"hello"}`, "content"},
	{"nested-shadow", `{"outer":{"content":"NOPE","x":1},"list":[{"content":"NO2"}],"content":"yes"}`, "content"},
	{"empty", `{"path":"/x","content":""}`, "content"},
	{"unicode", `{"content":"héllo 世界 🎩"}`, "content"},
	{"absent", `{"path":"/x","other":"v"}`, "content"},
	{"first-field", `{"content":"first","after":1}`, "content"},
	{"ws", "{  \"path\" : \"a\" , \"content\" : \"v\" }", "content"},
	{"nested-array-mixed", `{"arr":[1,"x",{"content":"nope"},[{"content":"nope2"}]],"content":"ok"}`, "content"},
}

func TestStringFieldFullMatchesEncodingJSON(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(f.json), &m); err != nil {
				t.Fatalf("fixture unparseable: %v", err)
			}
			want, wantPresent := "", false
			if v, ok := m[f.field].(string); ok {
				want, wantPresent = v, true
			}
			got, gotPresent := StringField([]byte(f.json), f.field)
			if gotPresent != wantPresent {
				t.Fatalf("present: got %v want %v", gotPresent, wantPresent)
			}
			if got != want {
				t.Fatalf("value:\n got  %q\n want %q", got, want)
			}
		})
	}
}

func TestStringFieldMonotonicOverEveryPrefix(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v", r)
				}
			}()
			data := []byte(f.json)
			var prev string
			var seenPresent bool
			for i := 0; i <= len(data); i++ {
				got, present := StringField(data[:i], f.field)
				if seenPresent && !present {
					t.Fatalf("present regressed at i=%d prefix=%q", i, data[:i])
				}
				if present && !seenPresent {
					seenPresent = true
					prev = got
					continue
				}
				if present {
					if !hasPrefix(got, prev) {
						t.Fatalf("non-monotonic at i=%d:\n prev %q\n new  %q\n prefix=%q", i, prev, got, data[:i])
					}
					prev = got
				}
			}
		})
	}
}

func TestStringFieldNestedShadowNeverLeaks(t *testing.T) {
	data := []byte(`{"outer":{"content":"NOPE"},"content":"yes"}`)
	for i := 0; i <= len(data); i++ {
		got, present := StringField(data[:i], "content")
		if present {
			if contains(got, "NOPE") {
				t.Fatalf("leaked nested value at i=%d: %q", i, got)
			}
		}
	}
}

func TestStringFieldTruncatedMidEscape(t *testing.T) {
	// Ensure that ending exactly at "\" or "\u" or "\u00" gives value that is
	// a prefix of the fully-decoded value and never includes garbage.
	full := `{"content":"pre\u00e9post"}`
	data := []byte(full)
	target, _ := StringField(data, "content")
	if target != "preépost" {
		t.Fatalf("full decode: got %q", target)
	}
	for i := 0; i <= len(data); i++ {
		got, present := StringField(data[:i], "content")
		if present && !hasPrefix(target, got) {
			t.Fatalf("i=%d got %q not prefix of %q", i, got, target)
		}
	}
}

func TestStringFieldNonStringValue(t *testing.T) {
	if _, p := StringField([]byte(`{"content":123}`), "content"); p {
		t.Fatal("expected present=false for number value")
	}
	if _, p := StringField([]byte(`{"content":null}`), "content"); p {
		t.Fatal("expected present=false for null value")
	}
	if _, p := StringField([]byte(`{"content":{"x":1}}`), "content"); p {
		t.Fatal("expected present=false for object value")
	}
}

func TestStringFieldEmptyAndBogusInputs(t *testing.T) {
	inputs := []string{"", "{", `{"`, `{"content`, `{"content"`, `{"content":`, `{"content":"`, `not json`, `[]`, `null`}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on %q: %v", in, r)
				}
			}()
			got, present := StringField([]byte(in), "content")
			if in == `{"content":"` && (!present || got != "") {
				t.Fatalf(`expected present="" for %q, got present=%v val=%q`, in, present, got)
			}
		}()
	}
}

func FuzzStringFieldNoPanic(f *testing.F) {
	for _, x := range fixtures {
		f.Add([]byte(x.json), x.field)
	}
	f.Add([]byte(`{"a":"b","c":{"d":"e"}}`), "d")
	f.Fuzz(func(t *testing.T, data []byte, name string) {
		_, _ = StringField(data, name)
	})
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Fields is the generic walk the renderer uses to show a tool's arguments as
// they stream, so it is held to the same two properties StringField is: it
// agrees with encoding/json on whole input, and it never rewrites what it has
// already shown as the input grows.
func TestFieldsFullMatchesEncodingJSON(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(f.json), &m); err != nil {
				t.Fatalf("fixture unparseable: %v", err)
			}
			got := Fields([]byte(f.json))
			if len(got) != len(m) {
				t.Fatalf("Fields returned %d fields, want %d (%+v)", len(got), len(m), got)
			}
			for _, fld := range got {
				if !fld.Done {
					t.Errorf("%q: Done=false on whole input", fld.Name)
				}
				want, ok := m[fld.Name]
				if !ok {
					t.Errorf("Fields invented %q", fld.Name)
					continue
				}
				if s, isStr := want.(string); isStr && fld.Value != s {
					t.Errorf("%q: got %q, want %q", fld.Name, fld.Value, s)
				}
			}
		})
	}
}

// Monotonic over every prefix: the value shown for a field may only grow, and
// a field once listed may not vanish. A live view that rewrites itself is
// worse than one that lags.
func TestFieldsMonotonicOverEveryPrefix(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			seen := map[string]string{}
			var order []string
			for i := 0; i <= len(f.json); i++ {
				for _, fld := range Fields([]byte(f.json[:i])) {
					prev, had := seen[fld.Name]
					if !had {
						order = append(order, fld.Name)
					} else if len(fld.Value) < len(prev) || (len(fld.Value) >= len(prev) && fld.Value[:len(prev)] != prev) {
						t.Fatalf("prefix %d: %q went %q -> %q", i, fld.Name, prev, fld.Value)
					}
					seen[fld.Name] = fld.Value
				}
			}
			for i, name := range order {
				if i > 0 && name == order[i-1] {
					t.Fatalf("field %q listed twice in order %v", name, order)
				}
			}
		})
	}
}

// The truncation cases the renderer actually meets, mid-key and mid-value.
func TestFieldsTruncation(t *testing.T) {
	cases := []struct {
		in   string
		want []Field
	}{
		{`{`, nil},
		{`{"pa`, nil},
		{`{"path"`, nil},
		{`{"path":`, []Field{{Name: "path"}}},
		{`{"path":"/x`, []Field{{Name: "path", Value: "/x"}}},
		{`{"path":"/x"`, []Field{{Name: "path", Value: "/x", Done: true}}},
		{`{"path":"/x","n":2`, []Field{{Name: "path", Value: "/x", Done: true}, {Name: "n", Value: "2"}}},
		{`{"a":"1","b":"2"}`, []Field{{Name: "a", Value: "1", Done: true}, {Name: "b", Value: "2", Done: true}}},
		{`not json`, nil},
		{``, nil},
	}
	for _, tc := range cases {
		got := Fields([]byte(tc.in))
		if len(got) != len(tc.want) {
			t.Errorf("%q: got %+v, want %+v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i].Name != tc.want[i].Name || got[i].Value != tc.want[i].Value || got[i].Done != tc.want[i].Done {
				t.Errorf("%q field %d: got %+v, want %+v", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
