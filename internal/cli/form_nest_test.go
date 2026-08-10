package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

// The form is stored flat — one dotted key, one value, one patch record — but
// read as the tree those keys describe.
func TestNestSnapshotBuildsTheTree(t *testing.T) {
	snap := form.FromMap(map[string]json.RawMessage{
		"mantra":          json.RawMessage(`"sing"`),
		"system.model":    json.RawMessage(`"opus"`),
		"system.tags":     json.RawMessage(`[1,2]`),
		"skills.figaro":   json.RawMessage(`{"filePath":"/x"}`),
		"system.deep.a.b": json.RawMessage(`1`),
	})
	got, err := json.Marshal(nestSnapshot(snap))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mantra":"sing","skills":{"figaro":{"filePath":"/x"}},` +
		`"system":{"deep":{"a":{"b":1}},"model":"opus","tags":[1,2]}}`
	if string(got) != want {
		t.Errorf("nested form:\n got %s\nwant %s", got, want)
	}
}

// A key whose prefix is already a leaf keeps its dotted name: setting `a` must
// not make `a.b` unreadable, in either order.
func TestNestSnapshotKeepsCollidingKeysReadable(t *testing.T) {
	snap := form.FromMap(map[string]json.RawMessage{
		"a":   json.RawMessage(`"leaf"`),
		"a.b": json.RawMessage(`"under"`),
	})
	got, err := json.Marshal(nestSnapshot(snap))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"a":"leaf"`, `"a.b":"under"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("nested form %s missing %s", got, want)
		}
	}
}
