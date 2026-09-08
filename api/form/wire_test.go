package form_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// The on-disk format is sacred. the form channel, the figaro.form
// RPC response and store.formReduce all consume the flat object
// form, and the persistent-tree swap must not perturb a single byte of
// it. The oracle here is the representation the swap replaced: whatever
// json.Marshal does to a map[string]json.RawMessage is the truth, and a
// Snapshot must produce exactly that.
//
// The board under test is a real capture of the default outfit (37
// keys, 15KB, skill envelopes with em-dashes and embedded newlines,
// system.* scalars): see testdata/board-default.provenance.md: not a
// hand-written toy.

func realBoard(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/board-default.json")
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	require.Greater(t, len(m), 30, "fixture should be the real default board")
	return m
}

// TestRealBoard_MarshalsIdenticallyToMap is the headline compatibility
// assertion: the tree-backed Snapshot emits the same bytes the map did.
func TestRealBoard_MarshalsIdenticallyToMap(t *testing.T) {
	m := realBoard(t)

	s := form.FromMap(m)
	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Every key is readable back at its path with its bytes intact.
	var back form.Snapshot
	require.NoError(t, json.Unmarshal(got, &back))
	for k, want := range m {
		v, ok := back.Get(k)
		require.True(t, ok, "key %q did not survive", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}
}

// A board written flat by an older figaro must open, nest, and then be a
// fixed point: reading it back and re-writing it changes nothing further.
func TestRealBoard_OnDiskFlatBoardOpensAndIsStable(t *testing.T) {
	m := realBoard(t)
	onDisk, err := json.Marshal(m) // the flat layout State.Save used to write
	require.NoError(t, err)

	var s form.Snapshot
	require.NoError(t, json.Unmarshal(onDisk, &s))
	// Len counts addressable LEAVES, so a key whose value is an object
	// contributes one per field. Every written key is still readable at its
	// own path, which is what the loop below asserts.
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

	// Marshalling is a fixed point.
	var s form.Snapshot
	require.NoError(t, json.Unmarshal(got, &s))
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Equal(t, string(got), string(out))
}

// TestUnmarshal_NullAndEmpty, a `null` state or an empty object must
// both decode to an empty, usable board, as the map did.
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

// TestMarshal_InvalidValueStillErrors, a board holding non-JSON bytes
// failed to marshal before the swap and must keep failing, loudly,
// rather than silently emitting a null.
func TestMarshal_InvalidValueStillErrors(t *testing.T) {
	m := map[string]json.RawMessage{"bad": json.RawMessage(`{not json`)}
	_, mapErr := json.Marshal(m)
	require.Error(t, mapErr)
	_, snapErr := json.Marshal(form.FromMap(m))
	assert.Error(t, snapErr)
}

// --- The one sanctioned behaviour change ---

// TestDiff_KeyOrderOnlyChangeIsNotAChange documents the single visible
// difference the swap introduces: a value that changes only in object
// key order, insignificant whitespace or escape spelling now compares
// equal, so no <system-reminder> fires for it. Content is never altered
// : the stored bytes are whatever was written first.
func TestDiff_KeyOrderOnlyChangeIsNotAChange(t *testing.T) {
	prev := form.FromMap(map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":2}`),
	})
	next := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{ "b":2, "a":1 }`),
	}, nil))
	assert.True(t, next.Diff(prev).IsIdentity(),
		"a key-order-only rewrite must not read as a change")

	// The original bytes are retained, a no-op write does not perturb
	// the form channel.
	v, ok := next.Get("cfg")
	require.True(t, ok)
	assert.Equal(t, `{"a":1,"b":2}`, string(v))

	// A real content change still fires, and number spelling still counts.
	real := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":3}`),
	}, nil))
	assert.False(t, real.Diff(prev).IsIdentity())
	spelled := prev.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1.0,"b":2}`),
	}, nil))
	assert.False(t, spelled.Diff(prev).IsIdentity(), "1 and 1.0 are different edits")
}

// --- Clone is now free, and that must not leak mutability ---

func TestClone_IsIdentityAndStillSafe(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{"k": json.RawMessage(`"v1"`)})
	c := s.Clone()
	assert.Equal(t, s, c, "Clone of an immutable value is the identity")

	// Deriving from the clone must not disturb the original.
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
	// Same shape as diffing against the empty board, which is what this
	// replaced at the call sites.
	assert.Equal(t, p, s.Diff(form.Snapshot{}))
	assert.True(t, form.Snapshot{}.AsPatch().IsIdentity())
}

// TestSnapshotDirectCodecMatchesEncodingJSON pins the equivalence that
// lets the hot paths (store.formReduce, State.Open/Save) call
// MarshalJSON/UnmarshalJSON directly instead of going through
// json.Marshal/json.Unmarshal. encoding/json re-scans a Marshaler's
// output and pre-scans an Unmarshaler's input, a ~2x cost on a 15KB
// board for bytes that are identical. If this test ever fails, the
// direct calls must go back through encoding/json.
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

	// null is the one input where the two spellings could plausibly
	// diverge (encoding/json special-cases it for some types).
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

// leafMap is a patch's set values by path, for assertions.
func leafMap(p form.Patch) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, e := range p.Entries() {
		if !e.IsRemoval() {
			out[e.Key] = e.New
		}
	}
	return out
}
