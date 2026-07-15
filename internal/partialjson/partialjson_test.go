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
