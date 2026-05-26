package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseChalkboardPath(t *testing.T) {
	cases := []struct {
		in     string
		top    string
		path   []string
		hasErr bool
	}{
		{"system.credo", "system.credo", nil, false},
		{"system.tags[42].cache_control", "system.tags", []string{"42", "cache_control"}, false},
		{"system.tags[42]", "system.tags", []string{"42"}, false},
		{`foo["weird key"].bar`, "foo", []string{"weird key", "bar"}, false},
		{"[42]", "", nil, true},
		{"foo[", "", nil, true},
	}
	for _, tc := range cases {
		top, path, err := parseChalkboardPath(tc.in)
		if tc.hasErr {
			assert.Error(t, err, "input %q", tc.in)
			continue
		}
		require.NoError(t, err, "input %q", tc.in)
		assert.Equal(t, tc.top, top, "top %q", tc.in)
		assert.Equal(t, tc.path, path, "path %q", tc.in)
	}
}

func TestDeepSetJSON_Empty(t *testing.T) {
	out, err := deepSetJSON(nil, []string{"42", "cache_control"}, json.RawMessage(`"ephemeral"`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"42":{"cache_control":"ephemeral"}}`, string(out))
}

func TestDeepSetJSON_Merge(t *testing.T) {
	cur := json.RawMessage(`{"7":{"cache_control":"ephemeral"},"42":{"other":1}}`)
	out, err := deepSetJSON(cur, []string{"42", "cache_control"}, json.RawMessage(`"ephemeral"`))
	require.NoError(t, err)
	var got map[string]map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "ephemeral", got["42"]["cache_control"])
	assert.EqualValues(t, 1, got["42"]["other"])
	assert.Equal(t, "ephemeral", got["7"]["cache_control"])
}

func TestDeepSetJSON_OverwriteScalar(t *testing.T) {
	cur := json.RawMessage(`{"42":"oops"}`)
	out, err := deepSetJSON(cur, []string{"42", "cache_control"}, json.RawMessage(`"ephemeral"`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"42":{"cache_control":"ephemeral"}}`, string(out))
}

func TestDeepDeleteJSON_Leaf(t *testing.T) {
	cur := json.RawMessage(`{"42":{"cache_control":"ephemeral"},"7":{"cache_control":"ephemeral"}}`)
	out, dropTop, err := deepDeleteJSON(cur, []string{"42", "cache_control"})
	require.NoError(t, err)
	assert.False(t, dropTop, "still has the 7 entry")
	assert.JSONEq(t, `{"7":{"cache_control":"ephemeral"}}`, string(out),
		"empty parent ('42') must be pruned")
}

func TestDeepDeleteJSON_PrunesToTop(t *testing.T) {
	cur := json.RawMessage(`{"42":{"cache_control":"ephemeral"}}`)
	out, dropTop, err := deepDeleteJSON(cur, []string{"42", "cache_control"})
	require.NoError(t, err)
	assert.True(t, dropTop, "everything pruned -> caller drops top-level key")
	assert.Nil(t, out)
}

func TestDeepDeleteJSON_MissingPath(t *testing.T) {
	cur := json.RawMessage(`{"42":{"cache_control":"ephemeral"}}`)
	out, dropTop, err := deepDeleteJSON(cur, []string{"99", "cache_control"})
	require.NoError(t, err)
	assert.False(t, dropTop)
	assert.Equal(t, string(cur), string(out), "no-op when path absent")
}

func TestDeepDeleteJSON_EmptyInput(t *testing.T) {
	out, dropTop, err := deepDeleteJSON(nil, []string{"42", "cache_control"})
	require.NoError(t, err)
	assert.False(t, dropTop)
	assert.Nil(t, out)
}
