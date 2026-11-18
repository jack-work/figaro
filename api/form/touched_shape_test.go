package form

import (
	"encoding/json"
	"slices"
	"testing"
)

func snapOf(t *testing.T, keys map[string]string) Snapshot {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range keys {
		raw[k] = json.RawMessage(v)
	}
	return Snapshot{}.Apply(Build(Snapshot{}, raw, nil))
}

func faultPaths(fs []ShapeFault) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

// Defends P1b's contract with P5: a write is judged at the registry rows it
// reaches, above, at or below the patched path, never by the raw patch key.
func TestTouchedShapeFaultsJudgeTheRowsAWriteReaches(t *testing.T) {
	cases := []struct {
		name    string
		after   map[string]string
		touched []string
		want    []string
	}{
		{"a leaf under a shaped root", map[string]string{"system.retirement": `{"context_threshold":1000,"action":"stay"}`}, []string{"system.retirement.action"}, []string{"system.retirement"}},
		{"a parent over a shaped leaf", map[string]string{"system.fanout": `{"max_children":-1}`}, []string{"system.fanout"}, []string{"system.fanout.max_children"}},
		{"the exact row", map[string]string{"system.ttl": `"soon"`}, []string{"system.ttl"}, []string{"system.ttl"}},
		{"a placeholder instance", map[string]string{"system.environment.FOO": `7`}, []string{"system.environment.FOO"}, []string{"system.environment.FOO"}},
		{"a placeholder under a written parent", map[string]string{"system.environment": `{"FOO":7,"BAR":"ok"}`}, []string{"system.environment"}, []string{"system.environment.FOO"}},
		{"a leaf below a placeholder instance", map[string]string{"system.environment.FOO": `{"bar":1}`}, []string{"system.environment.FOO.bar"}, []string{"system.environment.FOO"}},
		{"an exact row beats its placeholder", map[string]string{"system.methods.enabled": `true`}, []string{"system.methods.enabled"}, nil},
		{"an untouched bad value is not this write's", map[string]string{"system.max_tokens": `0`, "mantra": `"x"`}, []string{"mantra"}, nil},
		{"a removal leaves nothing to judge", map[string]string{}, []string{"system.ttl"}, nil},
		{"good values pass", map[string]string{"system.ttl": `"2d"`, "system.fanout": `{"max_children":3,"mint":true}`}, []string{"system.ttl", "system.fanout"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := faultPaths(TouchedShapeFaults(snapOf(t, c.after), c.touched))
			if !slices.Equal(got, c.want) {
				t.Fatalf("faults at %v, want %v", got, c.want)
			}
		})
	}
}

func BenchmarkTouchedShapeFaultsFiveKeys(b *testing.B) {
	raw := map[string]json.RawMessage{
		"mantra": json.RawMessage(`"x"`), "system.cache_control": json.RawMessage(`"1h"`),
		"system.fanout": json.RawMessage(`{"max_children":3}`), "system.retirement": json.RawMessage(`{"context_threshold":1000,"ttl":"1h"}`),
		"system.environment": json.RawMessage(`{"A":"1","B":"2"}`), "notes.a": json.RawMessage(`"n"`),
	}
	after := Snapshot{}.Apply(Build(Snapshot{}, raw, nil))
	touched := []string{"mantra", "system.cache_control", "system.fanout.max_children", "system.retirement.ttl", "notes.a"}
	b.ReportAllocs()
	for b.Loop() {
		if len(TouchedShapeFaults(after, touched)) != 0 {
			b.Fatal("fault")
		}
	}
}
