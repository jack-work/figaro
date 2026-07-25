package chalkboard_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// The on-disk format is sacred. chalkboard.json, the figaro.chalkboard
// RPC response and store.chalkboardReduce all consume the flat object
// form, and the persistent-tree swap must not perturb a single byte of
// it. The oracle here is the representation the swap replaced: whatever
// json.Marshal does to a map[string]json.RawMessage is the truth, and a
// Snapshot must produce exactly that.
//
// The board under test is a real capture of the default loadout (37
// keys, 15KB, skill envelopes with em-dashes and embedded newlines,
// system.* scalars) — see testdata/board-default.provenance.md — not a
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

	wantBytes, err := json.Marshal(m)
	require.NoError(t, err)
	gotBytes, err := json.Marshal(chalkboard.FromMap(m))
	require.NoError(t, err)
	assert.Equal(t, string(wantBytes), string(gotBytes),
		"Snapshot must marshal byte-identically to the map it replaced")
}

// TestRealBoard_OnDiskRoundTripByteIdentical reads the on-disk byte
// layout (what chalkboard.State.Save writes) back through the new
// Snapshot and requires the re-serialisation to be byte-identical, twice
// over. This is the "existing chalkboard.json files keep working" gate.
func TestRealBoard_OnDiskRoundTripByteIdentical(t *testing.T) {
	m := realBoard(t)
	onDisk, err := json.Marshal(m) // exactly what State.Save writes today
	require.NoError(t, err)

	var s chalkboard.Snapshot
	require.NoError(t, json.Unmarshal(onDisk, &s))
	assert.Equal(t, len(m), s.Len())

	out, err := json.Marshal(s)
	require.NoError(t, err)
	require.True(t, bytes.Equal(onDisk, out),
		"unmarshal -> marshal of a real chalkboard.json must be byte-identical")

	var s2 chalkboard.Snapshot
	require.NoError(t, json.Unmarshal(out, &s2))
	out2, err := json.Marshal(s2)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(onDisk, out2), "the round trip must be a fixed point")

	// Every value survives verbatim, key by key.
	for k, want := range m {
		got, ok := s.Get(k)
		require.True(t, ok, "key %q lost", k)
		assert.JSONEq(t, string(want), string(got), "key %q", k)
	}
}

// TestRealBoard_PrettyPrintedRoundTrip covers the indenting encoder the
// CLI uses for `figaro chalkboard -j`: a MarshalJSON implementation must
// survive SetIndent identically to the map.
func TestRealBoard_PrettyPrintedRoundTrip(t *testing.T) {
	m := realBoard(t)

	encode := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		require.NoError(t, enc.Encode(v))
		return buf.String()
	}
	assert.Equal(t, encode(m), encode(chalkboard.FromMap(m)),
		"indented encoding must match the map's")
}

// TestWireShape_AdversarialValues pins the awkward cases against the map
// oracle: HTML-escapable bytes (encoding/json rewrites <, > and & inside
// a raw message, and that IS today's on-disk spelling), interior
// whitespace (compacted), unsorted nested keys and mixed escape
// spellings (both preserved), and number literals (preserved verbatim).
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
	want, err := json.Marshal(m)
	require.NoError(t, err)
	got, err := json.Marshal(chalkboard.FromMap(m))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	// And the round trip through the Snapshot is a fixed point.
	var s chalkboard.Snapshot
	require.NoError(t, json.Unmarshal(want, &s))
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(out))
}

// TestUnmarshal_NullAndEmpty — a `null` state or an empty object must
// both decode to an empty, usable board, as the map did.
func TestUnmarshal_NullAndEmpty(t *testing.T) {
	for _, in := range []string{`null`, `{}`} {
		var s chalkboard.Snapshot
		require.NoError(t, json.Unmarshal([]byte(in), &s), in)
		assert.Equal(t, 0, s.Len(), in)
		out, err := json.Marshal(s)
		require.NoError(t, err)
		assert.Equal(t, `{}`, string(out), in)
	}
}

// TestMarshal_InvalidValueStillErrors — a board holding non-JSON bytes
// failed to marshal before the swap and must keep failing, loudly,
// rather than silently emitting a null.
func TestMarshal_InvalidValueStillErrors(t *testing.T) {
	m := map[string]json.RawMessage{"bad": json.RawMessage(`{not json`)}
	_, mapErr := json.Marshal(m)
	require.Error(t, mapErr)
	_, snapErr := json.Marshal(chalkboard.FromMap(m))
	assert.Error(t, snapErr)
}

// --- The one sanctioned behaviour change ---

// TestDiff_KeyOrderOnlyChangeIsNotAChange documents the single visible
// difference the swap introduces: a value that changes only in object
// key order, insignificant whitespace or escape spelling now compares
// equal, so no <system-reminder> fires for it. Content is never altered
// — the stored bytes are whatever was written first.
func TestDiff_KeyOrderOnlyChangeIsNotAChange(t *testing.T) {
	prev := chalkboard.FromMap(map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":2}`),
	})
	next := prev.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"cfg": json.RawMessage(`{ "b":2, "a":1 }`),
	}})
	assert.True(t, next.Diff(prev).IsEmpty(),
		"a key-order-only rewrite must not read as a change")

	// The original bytes are retained — a no-op write does not perturb
	// chalkboard.json.
	v, ok := next.Get("cfg")
	require.True(t, ok)
	assert.Equal(t, `{"a":1,"b":2}`, string(v))

	// A real content change still fires, and number spelling still counts.
	real := prev.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1,"b":3}`),
	}})
	assert.False(t, real.Diff(prev).IsEmpty())
	spelled := prev.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"cfg": json.RawMessage(`{"a":1.0,"b":2}`),
	}})
	assert.False(t, spelled.Diff(prev).IsEmpty(), "1 and 1.0 are different edits")
}

// --- Clone is now free, and that must not leak mutability ---

func TestClone_IsIdentityAndStillSafe(t *testing.T) {
	s := chalkboard.FromMap(map[string]json.RawMessage{"k": json.RawMessage(`"v1"`)})
	c := s.Clone()
	assert.Equal(t, s, c, "Clone of an immutable value is the identity")

	// Deriving from the clone must not disturb the original.
	_ = c.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v2"`)}})
	v, _ := s.Get("k")
	assert.Equal(t, `"v1"`, string(v))
}

func TestAsPatch(t *testing.T) {
	s := chalkboard.FromMap(map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`"two"`),
	})
	p := s.AsPatch()
	assert.Nil(t, p.Remove)
	assert.Equal(t, map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`"two"`),
	}, p.Set)
	// Same shape as diffing against the empty board, which is what this
	// replaced at the call sites.
	assert.Equal(t, p, s.Diff(chalkboard.Snapshot{}))
	assert.True(t, chalkboard.Snapshot{}.AsPatch().IsEmpty())
}

// TestSnapshotDirectCodecMatchesEncodingJSON pins the equivalence that
// lets the hot paths (store.chalkboardReduce, State.Open/Save) call
// MarshalJSON/UnmarshalJSON directly instead of going through
// json.Marshal/json.Unmarshal. encoding/json re-scans a Marshaler's
// output and pre-scans an Unmarshaler's input — a ~2x cost on a 15KB
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
		s := chalkboard.FromMap(m)

		viaJSON, err := json.Marshal(s)
		require.NoError(t, err)
		direct, err := s.MarshalJSON()
		require.NoError(t, err)
		assert.Equal(t, string(viaJSON), string(direct), "board %d: marshal", i)

		var a, b chalkboard.Snapshot
		require.NoError(t, json.Unmarshal(viaJSON, &a))
		require.NoError(t, b.UnmarshalJSON(viaJSON))
		assert.Equal(t, content2(t, a), content2(t, b), "board %d: unmarshal", i)
	}

	// null is the one input where the two spellings could plausibly
	// diverge (encoding/json special-cases it for some types).
	var a, b chalkboard.Snapshot
	require.NoError(t, json.Unmarshal([]byte(`null`), &a))
	require.NoError(t, b.UnmarshalJSON([]byte(`null`)))
	assert.Equal(t, 0, a.Len())
	assert.Equal(t, 0, b.Len())
}

func content2(t *testing.T, s chalkboard.Snapshot) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}
