package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// The form channel is full of flat patches. A structural decode of one must
// not silently produce Identity: the whole board would reduce to nothing.
func TestLegacyFlatPatchStillApplies(t *testing.T) {
	raw := []byte(`{"set":{"system.model":"claude-opus-5","mantra":"hello","skills.howto":{"a":1}},"remove":["stale"]}`)
	var p form.Patch
	require.NoError(t, json.Unmarshal(raw, &p))
	require.False(t, p.IsEmpty(), "a flat patch decoded to Identity: history would vanish")

	base := form.FromMap(map[string]json.RawMessage{"stale": json.RawMessage(`"x"`)})
	got := base.Apply(p)

	require.Equal(t, "claude-opus-5", *got.Lookup("system.model"))
	require.Equal(t, "hello", *got.Lookup("mantra"))
	require.False(t, got.Has("stale"), "the removal did not take")

	// And it is addressable structurally now.
	v, ok := got.Get("skills.howto")
	require.True(t, ok)
	require.JSONEq(t, `{"a":1}`, string(v))
}

// A flat board on disk nests on read.
func TestLegacyFlatSnapshotNests(t *testing.T) {
	var s form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`{"system.model":"m","system.cwd":"/tmp","mantra":"hi"}`), &s))
	require.Equal(t, "m", *s.Lookup("system.model"))
	require.Equal(t, "/tmp", *s.Lookup("system.cwd"))
	require.Equal(t, "hi", *s.Lookup("mantra"))

	// Nested for real: system is a branch with two members.
	v, ok := s.Get("system")
	require.True(t, ok)
	require.JSONEq(t, `{"model":"m","cwd":"/tmp"}`, string(v))
}

// The collision that flatness could not express.
func TestLeafAndPrefixBothAddressable(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"skills.howto":    json.RawMessage(`"the skill"`),
		"skills.howto.md": json.RawMessage(`"the other one"`),
	})
	a, ok := s.Get("skills.howto")
	require.True(t, ok)
	require.JSONEq(t, `"the skill"`, string(a))
	b, ok := s.Get("skills.howto.md")
	require.True(t, ok)
	require.JSONEq(t, `"the other one"`, string(b))
}
