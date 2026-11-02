package formgen_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func snapOf(t *testing.T, s string) form.Snapshot {
	t.Helper()
	var x form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(s), &x))
	return x
}

// An AVL delete of a node with two children rebuilds it from its in-order
// successor; a successor that is a branch must keep its subtree. Here "b"
// sits between "a" and "c", and "c" holds a subtree.
func TestTreeDelete_KeepsTheSuccessorsSubtree(t *testing.T) {
	s := snapOf(t, `{"a":1,"b":2,"c":{"d":3}}`)
	got := s.Apply(form.Build(s, nil, []string{"b"}))
	require.JSONEq(t, `{"a":1,"c":{"d":3}}`, show(got))
}

// Setting a key that holds a subtree to a scalar is what Build makes against
// the real state (an Update carrying a Scalar). The tree path must apply it,
// not drop it: a replica's edits are always built against its state.
func TestTreeApply_ScalarOverBranch(t *testing.T) {
	s := snapOf(t, `{"c":{"a":{"a":false}}}`)
	p := form.Build(s, map[string]json.RawMessage{"c": json.RawMessage(`80`)}, nil)
	require.JSONEq(t, `{"c":80}`, show(s.Apply(p)))
	v, err := p.Apply(s.Root())
	require.NoError(t, err)
	require.JSONEq(t, `{"c":80}`, string(v.Raw()))
}

// null is a scalar. A branch set to null holds null, and diffing to and from
// an empty object is not identity.
func TestTreeApply_NullIsAScalar(t *testing.T) {
	s := snapOf(t, `{"a":{"b":1}}`)
	p := form.Build(s, map[string]json.RawMessage{"a": json.RawMessage(`null`)}, nil)
	require.JSONEq(t, `{"a":null}`, show(s.Apply(p)))
}

func TestTreeDiff_SeesAnEmptyObject(t *testing.T) {
	a := snapOf(t, `{}`)
	b := snapOf(t, `{"b":{"c":{}}}`)
	require.JSONEq(t, show(b), show(a.Apply(b.Diff(a))))
}
