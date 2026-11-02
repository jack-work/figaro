package form_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

// Setting a key that holds a subtree to a scalar is an Update carrying a
// Scalar. The tree path handed it to the subtree, failed to read the scalar as
// an object, and dropped the write; the server then answered "unchanged".
func TestApply_ScalarReplacesABranch(t *testing.T) {
	s := deleteSnap(t, `{"c":{"a":{"a":false}},"d":1}`)
	p := form.Build(s, map[string]json.RawMessage{"c": json.RawMessage(`80`)}, nil)
	require.JSONEq(t, `{"c":80,"d":1}`, deleteShow(t, s.Apply(p)))
	v, err := p.Apply(s.Root())
	require.NoError(t, err)
	require.JSONEq(t, `{"c":80,"d":1}`, string(v.Raw()), "the value path agrees")
}

// A branch replaced by another object through a scalar keeps the new shape.
func TestApply_ScalarReplacesABranchWithAnObject(t *testing.T) {
	s := deleteSnap(t, `{"c":{"a":1,"b":2}}`)
	p := form.Patch{Object: &form.ObjectPatch{Update: map[string]form.Patch{
		"c": {Scalar: &form.ScalarPatch{After: form.NewValue(json.RawMessage(`{"z":3}`))}},
	}}}
	require.JSONEq(t, `{"c":{"z":3}}`, deleteShow(t, s.Apply(p)))
}

// The shape found in live forms: a key holding {} never took a later string,
// so aria_id and mantra read {} for ever.
func TestApply_ScalarReplacesAnEmptyObject(t *testing.T) {
	s := deleteSnap(t, `{"aria_id":{},"mantra":{}}`)
	p := form.Build(s, map[string]json.RawMessage{"aria_id": json.RawMessage(`"0bbd2960"`), "mantra": json.RawMessage(`"m"`)}, nil)
	require.JSONEq(t, `{"aria_id":"0bbd2960","mantra":"m"}`, deleteShow(t, s.Apply(p)))
}
