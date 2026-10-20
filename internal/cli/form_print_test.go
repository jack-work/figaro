package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestPrintedFormIsTheTree(t *testing.T) {
	snap := form.FromMap(map[string]json.RawMessage{
		"mantra":          json.RawMessage(`"sing"`),
		"system.model":    json.RawMessage(`"opus"`),
		"system.tags":     json.RawMessage(`[1,2]`),
		"skills.figaro":   json.RawMessage(`{"filePath":"/x"}`),
		"system.deep.a.b": json.RawMessage(`1`),
	})
	got, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mantra":"sing","skills":{"figaro":{"filePath":"/x"}},` +
		`"system":{"deep":{"a":{"b":1}},"model":"opus","tags":[1,2]}}`
	if string(got) != want {
		t.Errorf("printed form:\n got %s\nwant %s", got, want)
	}
}

// A dot inside a key is not a level. Re-splitting flattened paths printed
// claude-opus-5.5 as a child "5" of claude-opus-5, in `state -j`, `form show`
// and the S pit alike.
func dottedCatalog() form.Snapshot {
	raw := json.RawMessage(`{"models":{"claude-opus-5":{"n":1},"claude-opus-5.5":{"n":2}}}`)
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"copilot": raw}, nil))
}

func TestPrintedFormKeepsDottedKeysWhole(t *testing.T) {
	got, err := json.Marshal(dottedCatalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"copilot":{"models":{"claude-opus-5":{"n":1},"claude-opus-5.5":{"n":2}}}}`
	if string(got) != want {
		t.Errorf("printed form:\n got %s\nwant %s", got, want)
	}
}

func TestFormTreeKeepsDottedKeysWhole(t *testing.T) {
	root := buildFormTree(dottedCatalog())
	var labels []string
	var walk func(n *formNode)
	walk = func(n *formNode) {
		for _, c := range n.children {
			labels = append(labels, strings.Repeat(" ", c.depth)+c.label)
			walk(c)
		}
	}
	walk(root)
	want := []string{" copilot", "  models", "   claude-opus-5", "    n", "   claude-opus-5.5", "    n"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("tree:\n got %q\nwant %q", labels, want)
	}
}
