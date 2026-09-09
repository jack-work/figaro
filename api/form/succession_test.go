package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// A key that is a proper prefix of another, written deliberately: succession
// is prose AND a namespace of sub-notes.
func TestParentAndChildKeysBothSurvive(t *testing.T) {
	src := map[string]json.RawMessage{
		"succession":           json.RawMessage(`"the protocol"`),
		"succession.rule":      json.RawMessage(`"you are a seat"`),
		"succession.authority": json.RawMessage(`"only the target may write"`),
	}
	s := form.FromMap(src)
	for k, want := range src {
		got, ok := s.Get(k)
		require.True(t, ok, "%s unreachable", k)
		assert.JSONEq(t, string(want), string(got), k)
	}

	// And across a reload, which is where the shape has to agree with itself.
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	var back form.Snapshot
	require.NoError(t, json.Unmarshal(raw, &back))
	for k, want := range src {
		got, ok := back.Get(k)
		require.True(t, ok, "%s did not survive a reload", k)
		assert.JSONEq(t, string(want), string(got), k)
	}
}
