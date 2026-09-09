package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// A flat patch no longer decodes: cmd/figaro-form-migrate rewrites the form
// channel in place, replaying each node so every record is converted against
// the board it lands on. That is what makes the result invertible, which a
// decoder with no board could never be.

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
