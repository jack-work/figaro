package aria

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func TestForkedFromOf(t *testing.T) {
	set := func(key, value string, kind livedoc.FormKind, event livedoc.FormEvent) map[string]livedoc.FormDelta {
		return map[string]livedoc.FormDelta{"a1." + key: {Value: json.RawMessage(value), Kind: kind, Event: event, Form: "a1"}}
	}
	for _, tc := range []struct {
		name   string
		deltas map[string]livedoc.FormDelta
		want   string
	}{
		{"runtime key", set("runtime.forked_from", `"90ec6584"`, livedoc.FormBound, livedoc.FormSet), "90ec6584"},
		{"legacy key", set("system.forked_from", `"90ec6584"`, livedoc.FormBound, livedoc.FormSet), "90ec6584"},
		{"a studied form is not this aria's birth", set("runtime.forked_from", `"90ec6584"`, livedoc.FormStudied, livedoc.FormSet), ""},
		{"a removal is not a birth", set("runtime.forked_from", ``, livedoc.FormBound, livedoc.FormRemoved), ""},
		{"another key", set("mantra", `"90ec6584"`, livedoc.FormBound, livedoc.FormSet), ""},
		{"none", nil, ""},
	} {
		if got := ForkedFromOf(tc.deltas); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
