package form_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// The on-disk form is a flat object; the oracle is what json.Marshal does to
// a map[string]json.RawMessage. testdata/board-default.json is a real capture.

func realBoard(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/board-default.json")
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	require.Greater(t, len(m), 30, "fixture should be the real default board")
	return m
}

func TestRealBoard_MarshalsIdenticallyToMap(t *testing.T) {
	m := realBoard(t)

	s := form.FromMap(m)
	got, err := json.Marshal(s)
	require.NoError(t, err)

	var back form.Snapshot
	require.NoError(t, json.Unmarshal(got, &back))
	for k, want := range m {
		v, ok := back.Get(k)
		require.True(t, ok, "key %q did not survive", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}
}

func TestRealBoard_OnDiskFlatBoardOpensAndIsStable(t *testing.T) {
	m := realBoard(t)
	onDisk, err := json.Marshal(m)
	require.NoError(t, err)

	var s form.Snapshot
	require.NoError(t, json.Unmarshal(onDisk, &s))
	// Len counts leaves, so an object-valued key contributes one per field.
	assert.GreaterOrEqual(t, s.Len(), len(m))
	for k, want := range m {
		v, ok := s.Get(k)
		require.True(t, ok, "key %q did not survive", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}

	first, err := json.Marshal(s)
	require.NoError(t, err)
	var again form.Snapshot
	require.NoError(t, json.Unmarshal(first, &again))
	second, err := json.Marshal(again)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second),
		"re-reading a board and writing it back must change nothing")
}

func TestWireShape_AdversarialValues(t *testing.T) {
	m := map[string]json.RawMessage{
		"html":         json.RawMessage(`"<script>a & b</script>"`),
		"html.nested":  json.RawMessage(`{"tag":"<b>","amp":"&"}`),
		"spaced":       json.RawMessage("{ \"x\" : 1,\n  \"y\" : [ 1, 2 ] }"),
		"unsorted":     json.RawMessage(`{"z":1,"a":2,"m":{"q":1,"b":2}}`),
		"escapes":      json.RawMessage(`"\u00e9 é \u003c < \\ \" \n"`),
		"numbers":      json.RawMessage(`[1,1.0,1e2,100,0.000,123456789012345678901234567890]`),
		"unicode":      json.RawMessage(`"caffè ☕ \ud83d\ude00"`),
		"empty.obj":    json.RawMessage(`{}`),
		"empty.arr":    json.RawMessage(`[]`),
		"nul":          json.RawMessage(`null`),
		"deep":         json.RawMessage(`{"a":{"b":{"c":{"d":[{"e":1}]}}}}`),
		"key\"with\\q": json.RawMessage(`"tricky"`),
		"key<html>":    json.RawMessage(`"tricky too"`),
	}
	board := form.FromMap(m)
	got, err := json.Marshal(board)
	require.NoError(t, err)
	for k, want := range m {
		v, ok := board.Get(k)
		require.True(t, ok, "key %q did not survive", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}

	var s form.Snapshot
	require.NoError(t, json.Unmarshal(got, &s))
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Equal(t, string(got), string(out))
}

func TestUnmarshal_NullAndEmpty(t *testing.T) {
	for _, in := range []string{`null`, `{}`} {
		var s form.Snapshot
		require.NoError(t, json.Unmarshal([]byte(in), &s), in)
		assert.Equal(t, 0, s.Len(), in)
		out, err := json.Marshal(s)
		require.NoError(t, err)
		assert.Equal(t, `{}`, string(out), in)
	}
}

func TestMarshal_InvalidValueStillErrors(t *testing.T) {
	m := map[string]json.RawMessage{"bad": json.RawMessage(`{not json`)}
	_, mapErr := json.Marshal(m)
	require.Error(t, mapErr)
	_, snapErr := json.Marshal(form.FromMap(m))
	assert.Error(t, snapErr)
}

// A value that changes only in key order, whitespace or escape spelling is
// not a change; the stored bytes are whatever was written first.
func TestDiff_KeyOrderOnlyChangeIsNotAChange(t *testing.T) {
	prev := form.FromMap(map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":2}`),
	})
	next := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{ "b":2, "a":1 }`),
	}, nil))
	assert.True(t, next.Diff(prev).IsIdentity(),
		"a key-order-only rewrite must not read as a change")

	v, ok := next.Get("cfg")
	require.True(t, ok)
	assert.Equal(t, `{"a":1,"b":2}`, string(v))

	real := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":3}`),
	}, nil))
	assert.False(t, real.Diff(prev).IsIdentity())
	spelled := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1.0,"b":2}`),
	}, nil))
	assert.False(t, spelled.Diff(prev).IsIdentity(), "1 and 1.0 are different edits")
}

func TestClone_IsIdentityAndStillSafe(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{"k": json.RawMessage(`"v1"`)})
	c := s.Clone()
	assert.Equal(t, s, c, "Clone of an immutable value is the identity")

	_ = c.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"k": json.RawMessage(`"v2"`)}, nil))
	v, _ := s.Get("k")
	assert.Equal(t, `"v1"`, string(v))
}

func TestAsPatch(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`"two"`),
	})
	p := s.AsPatch()
	for _, e := range p.Entries() {
		assert.False(t, e.IsRemoval(), "AsPatch builds from empty: nothing to remove")
	}
	assert.Equal(t, map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`"two"`),
	}, leafMap(p))
	assert.Equal(t, p, s.Diff(form.Snapshot{}))
	assert.True(t, form.Snapshot{}.AsPatch().IsIdentity())
}

// The hot paths call MarshalJSON/UnmarshalJSON directly; if this fails they
// must go back through encoding/json.
func TestSnapshotDirectCodecMatchesEncodingJSON(t *testing.T) {
	boards := []map[string]json.RawMessage{
		realBoard(t),
		{
			"html":     json.RawMessage(`"<a>&</a>"`),
			"spaced":   json.RawMessage("{ \"x\" : 1 }"),
			"unsorted": json.RawMessage(`{"z":1,"a":2}`),
			"escapes":  json.RawMessage(`"\u00e9 é \u003c <"`),
			"numbers":  json.RawMessage(`[1,1.0,1e2,0.000]`),
			"nul":      json.RawMessage(`null`),
		},
		{},
	}
	for i, m := range boards {
		s := form.FromMap(m)

		viaJSON, err := json.Marshal(s)
		require.NoError(t, err)
		direct, err := s.MarshalJSON()
		require.NoError(t, err)
		assert.JSONEq(t, string(viaJSON), string(direct), "board %d: marshal", i)

		var a, b form.Snapshot
		require.NoError(t, json.Unmarshal(viaJSON, &a))
		require.NoError(t, b.UnmarshalJSON(viaJSON))
		assert.Equal(t, content2(t, a), content2(t, b), "board %d: unmarshal", i)
	}

	var a, b form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`null`), &a))
	require.NoError(t, b.UnmarshalJSON([]byte(`null`)))
	assert.Equal(t, 0, a.Len())
	assert.Equal(t, 0, b.Len())
}

func content2(t *testing.T, s form.Snapshot) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

func leafMap(p form.Patch) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, e := range p.Entries() {
		if !e.IsRemoval() {
			out[e.Key] = e.New
		}
	}
	return out
}
