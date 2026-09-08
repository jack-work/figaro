package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// Each test here fails against the flat implementation. They are the argument
// for the change, stated as things that either work or do not.

// A key whose own name contains a dot is addressable alongside a key that is
// its prefix. Gluck's store holds both: skills.howto is a skill, and
// skills.howto.md is a different skill.
func TestCollidingKeysAreBothAddressable(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"skills.howto":    json.RawMessage(`{"filePath":"/a.md"}`),
		"skills.howto.md": json.RawMessage(`{"filePath":"/b.md"}`),
		"succession":      json.RawMessage(`["one"]`),
		"succession.plan": json.RawMessage(`"handover"`),
	})
	for k, want := range map[string]string{
		"skills.howto":    `{"filePath":"/a.md"}`,
		"skills.howto.md": `{"filePath":"/b.md"}`,
		"succession":      `["one"]`,
		"succession.plan": `"handover"`,
	} {
		got, ok := s.Get(k)
		require.True(t, ok, "%s is unreachable", k)
		assert.JSONEq(t, want, string(got), k)
	}
}

// One field of a large value changes without rewriting the value. The credo on
// Gluck's board is 6.5KB and was rewritten whole to change one character.
func TestOneFieldOfALargeValueIsItsOwnChange(t *testing.T) {
	big := make([]byte, 6000)
	for i := range big {
		big[i] = 'x'
	}
	prev := form.FromMap(map[string]json.RawMessage{
		"system.credo": mustJSON(t, map[string]string{
			"content": string(big), "filePath": "/credo.md", "frontmatter": "name: figaro",
		}),
	})
	next := prev.SetPath("system.credo.filePath", json.RawMessage(`"/moved.md"`))

	p := next.Diff(prev)
	es := p.Entries()
	require.Len(t, es, 1, "one field changed, so one entry: %v", es)
	assert.Equal(t, "system.credo.filePath", es[0].Key)

	// The 6KB of content is not in the patch.
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Less(t, len(raw), 300, "the patch carries the untouched content")

	// And it still applies.
	got, ok := prev.Apply(p).Get("system.credo.filePath")
	require.True(t, ok)
	assert.JSONEq(t, `"/moved.md"`, string(got))
}

// Protection sees a write at any depth. A flat patch could only name top-level
// keys, so a write into a protected subtree was invisible to CheckWritable.
func TestProtectionSeesANestedWrite(t *testing.T) {
	var managed string
	for _, k := range form.WellKnownKeys() {
		if k.Mode == form.KeySystemManaged {
			managed = k.Key
			break
		}
	}
	require.NotEmpty(t, managed)

	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		managed: json.RawMessage(`"forged"`),
	}, nil)
	assert.Error(t, form.CheckWritable(p, false),
		"a write to %s must be refused however deep it sits", managed)
}

// A patch can be undone from itself, with no snapshot in hand.
func TestAPatchCarriesItsOwnUndo(t *testing.T) {
	before := form.FromMap(map[string]json.RawMessage{
		"mantra":       json.RawMessage(`"first"`),
		"system.model": json.RawMessage(`"opus"`),
		"doomed":       json.RawMessage(`"here"`),
	})
	after := before.
		SetPath("mantra", json.RawMessage(`"second"`)).
		SetPath("system.cwd", json.RawMessage(`"/tmp"`)).
		DeletePath("doomed")

	p := after.Diff(before)
	restored := after.Apply(p.Inverse())

	for _, k := range []string{"mantra", "system.model", "doomed"} {
		want, _ := before.Get(k)
		got, ok := restored.Get(k)
		require.True(t, ok, "%s was not restored", k)
		assert.JSONEq(t, string(want), string(got), k)
	}
	_, hasNew := restored.Get("system.cwd")
	assert.False(t, hasNew, "undo must remove what the patch added")
}

// Two writers touching different fields of one parent do not clobber each
// other. This is what dropped system.credo when an outfit set system.model.
func TestSiblingWritesUnderOneParentCompose(t *testing.T) {
	a := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.credo": json.RawMessage(`"largo"`)}, nil)
	b := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"opus"`)}, nil)

	board := form.Snapshot{}.Apply(a).Apply(b)
	credo, ok := board.Get("system.credo")
	require.True(t, ok, "the second write clobbered the first")
	assert.JSONEq(t, `"largo"`, string(credo))
	model, ok := board.Get("system.model")
	require.True(t, ok)
	assert.JSONEq(t, `"opus"`, string(model))
}

// A value that is not valid JSON is refused, not silently dropped.
func TestAnInvalidValueIsRefusedNotDropped(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{"bad": json.RawMessage(`{not json`)})
	_, err := json.Marshal(s)
	assert.Error(t, err, "a board that cannot be serialised must say so")
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
