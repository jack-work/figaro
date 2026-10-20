package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// Issue 43: a member written through a non-object leaf must land nested,
// never as a flat "a.b" sibling that no JSON reader finds at a.b.
func TestSetPathThroughLeafNests(t *testing.T) {
	for name, board := range map[string]string{
		"null":   `{"name":"f","system":null}`,
		"string": `{"name":"f","system":"prose"}`,
		"number": `{"name":"f","system":7}`,
		"array":  `{"name":"f","system":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var s form.Snapshot
			require.NoError(t, json.Unmarshal([]byte(board), &s))

			next := s.SetPath("system.ebac", json.RawMessage(`{"round":7}`))

			out, err := json.Marshal(next)
			require.NoError(t, err)
			assert.JSONEq(t, `{"name":"f","system":{"ebac":{"round":7}}}`, string(out))

			got, ok := next.Get("system.ebac.round")
			require.True(t, ok)
			assert.JSONEq(t, `7`, string(got))

			applied := s.Apply(next.Diff(s))
			back, err := json.Marshal(applied)
			require.NoError(t, err)
			assert.JSONEq(t, string(out), string(back), "the diff must carry the nested write")
		})
	}
}

// The control from the issue: through an object the write already nests.
func TestSetPathThroughBranchKeepsSiblings(t *testing.T) {
	var s form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`{"system":{"ttl":"1h"}}`), &s))
	out, err := json.Marshal(s.SetPath("system.ebac", json.RawMessage(`{"round":7}`)))
	require.NoError(t, err)
	assert.JSONEq(t, `{"system":{"ttl":"1h","ebac":{"round":7}}}`, string(out))
}

// A flat key set naming both a scalar and a member under it cannot describe
// one JSON object; the member wins, whatever order the map yields.
func TestFlatScalarAndMemberNestTheMember(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"succession":      json.RawMessage(`"the protocol"`),
		"succession.rule": json.RawMessage(`"you are a seat"`),
	})
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"succession":{"rule":"you are a seat"}}`, string(out))

	var back form.Snapshot
	require.NoError(t, json.Unmarshal(out, &back))
	got, ok := back.Get("succession.rule")
	require.True(t, ok)
	assert.JSONEq(t, `"you are a seat"`, string(got))
}
