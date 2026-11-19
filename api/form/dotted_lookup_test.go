package form_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

// A dotted key sorting between a member and the path below it hid that
// member: "m.claude-opus-5.context" missed once "claude-opus-5-5" and
// "claude-opus-5.5" sat beside "claude-opus-5", so the catalog read capacity
// unknown for claude-opus-5.
func TestGet_MemberBesideDottedSiblings(t *testing.T) {
	var s form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`{"m":{
		"claude-opus-5":{"context":{"default":1000000}},
		"claude-opus-5-5":{"context":{"default":2}},
		"claude-opus-5.5":{"context":{"default":3}},
		"explabs/glm-5.3":{"context":{"default":4}},
		"explabs/glm-5.3-uncensored":{"context":{"default":5}},
		"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7
	}}`), &s))
	for key, want := range map[string]string{
		"m.claude-opus-5.context.default":              `1000000`,
		"m.claude-opus-5-5.context.default":            `2`,
		"m.claude-opus-5.5.context.default":            `3`,
		"m.explabs/glm-5.3.context.default":            `4`,
		"m.explabs/glm-5.3-uncensored.context.default": `5`,
	} {
		got, ok := s.Get(key)
		require.True(t, ok, key)
		require.JSONEq(t, want, string(got), key)
	}
}
