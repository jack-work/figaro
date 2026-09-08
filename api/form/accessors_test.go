package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

func TestGet_PresentAndAbsent(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"cwd": raw(t, "/foo"),
	})
	v, ok := s.Get("cwd")
	assert.True(t, ok)
	assert.Equal(t, raw(t, "/foo"), v)

	v, ok = s.Get("missing")
	assert.False(t, ok)
	assert.Nil(t, v)
}

func TestGet_NilSnapshot(t *testing.T) {
	var s form.Snapshot
	v, ok := s.Get("anything")
	assert.False(t, ok)
	assert.Nil(t, v)
	assert.False(t, s.Has("anything"))
	assert.Equal(t, 0, s.Len())
	for range s.All() {
		t.Fatal("nil snapshot must not yield entries")
	}
}

func TestHasAndLen(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"a": raw(t, 1),
		"b": raw(t, 2),
	})
	assert.True(t, s.Has("a"))
	assert.False(t, s.Has("c"))
	assert.Equal(t, 2, s.Len())
	assert.Equal(t, 0, form.Snapshot{}.Len())
}

func TestAll_LexicalKeyOrder(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"zeta":         raw(t, "z"),
		"alpha":        raw(t, "a"),
		"Beta":         raw(t, "B"),
		"system.credo": raw(t, "c"),
		"a":            raw(t, "1"),
	})
	var keys []string
	var vals []string
	for k, v := range s.All() {
		keys = append(keys, k)
		vals = append(vals, string(v))
	}
	// Byte-wise lexical order: uppercase sorts before lowercase.
	assert.Equal(t, []string{"Beta", "a", "alpha", "system.credo", "zeta"}, keys)
	assert.Equal(t, []string{`"B"`, `"1"`, `"a"`, `"c"`, `"z"`}, vals)

	// Order must be stable across repeated iteration.
	for i := 0; i < 20; i++ {
		var again []string
		for k := range s.All() {
			again = append(again, k)
		}
		require.Equal(t, keys, again, "All must be deterministic")
	}
}

func TestAll_EarlyBreak(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"a": raw(t, 1),
		"b": raw(t, 2),
		"c": raw(t, 3),
	})
	var keys []string
	for k := range s.All() {
		keys = append(keys, k)
		if len(keys) == 2 {
			break
		}
	}
	assert.Equal(t, []string{"a", "b"}, keys)
}

func TestFromMap_CopiesNotAliases(t *testing.T) {
	m := map[string]json.RawMessage{
		"k": json.RawMessage(`"v1"`),
	}
	s := form.FromMap(m)

	// Mutating the source map must not be visible through the snapshot.
	m["k"] = json.RawMessage(`"v2"`)
	m["new"] = json.RawMessage(`"n"`)
	delete(m, "gone")

	v, ok := s.Get("k")
	require.True(t, ok)
	assert.Equal(t, `"v1"`, string(v))
	assert.False(t, s.Has("new"))
	assert.Equal(t, 1, s.Len())
}

func TestFromMap_CopiesValueBytes(t *testing.T) {
	buf := json.RawMessage(`"v1"`)
	m := map[string]json.RawMessage{"k": buf}
	s := form.FromMap(m)

	// Mutating the value bytes in place must not be visible either
	// (Clone has the same depth semantics).
	copy(buf, `"XX"`)

	v, _ := s.Get("k")
	assert.Equal(t, `"v1"`, string(v))
}

func TestFromMap_Nil(t *testing.T) {
	s := form.FromMap(nil)
	assert.Equal(t, 0, s.Len())
	assert.False(t, s.Has("k"))
	// A nil-sourced snapshot must still be usable as a Diff/Apply base.
	p := form.FromMap(map[string]json.RawMessage{"k": raw(t, "v")}).Diff(s)
	e, ok := p.Entry("k")
	assert.True(t, ok)
	assert.Equal(t, raw(t, "v"), json.RawMessage(e.New))
}

// --- JSON wire shape: the form channel on disk, the RPC FormResponse and
// formReduce in internal/store all read what Snapshot marshals. ---

// realBoardMap is a non-trivial board: dotted keys, awkward key text, every
// JSON kind, and values that must survive verbatim.
func realBoardMap(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"a key with spaces":   raw(t, "ok"),
		"count":               json.RawMessage(`42`),
		"cwd":                 raw(t, "/home/figaro"),
		"empty.string":        raw(t, ""),
		"flag":                json.RawMessage(`true`),
		"list":                json.RawMessage(`[1,"two",{"three":3}]`),
		"model":               raw(t, "claude-opus-4-6"),
		"nested":              json.RawMessage(`{"b":2,"a":[1,2,{"c":3}]}`),
		"nil":                 json.RawMessage(`null`),
		"quote\"in\\the\nkey": raw(t, "tricky"),
		"skills.figaro":       json.RawMessage(`{"filePath":"/x/y.md","frontmatter":"name: figaro"}`),
		"system.credo":        raw(t, "largo al factotum"),
		"unicode":             json.RawMessage(`"caff\u00e8 \u2615 \u00e9"`),
	}
}

func TestSnapshot_MarshalsAsATreeAndReadsBackByPath(t *testing.T) {
	src := realBoardMap(t)
	s := form.FromMap(src)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The wire shape is the tree the keys describe, so a dotted key is not
	// a member of the root object.
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &root))
	for k := range root {
		assert.NotContains(t, k, ".", "a dotted key survived into the wire shape")
	}

	// Every key is readable back at its path, byte for byte.
	back := form.Snapshot{}
	require.NoError(t, json.Unmarshal(got, &back))
	for k, want := range src {
		v, ok := back.Get(k)
		require.True(t, ok, "key %q did not survive the round trip", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}
}

func TestSnapshot_JSONRoundTripIsStable(t *testing.T) {
	src := realBoardMap(t)
	flat, err := json.Marshal(src)
	require.NoError(t, err)

	var s form.Snapshot
	require.NoError(t, json.Unmarshal(flat, &s))
	first, err := json.Marshal(s)
	require.NoError(t, err)

	var again form.Snapshot
	require.NoError(t, json.Unmarshal(first, &again))
	second, err := json.Marshal(again)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second), "the wire shape must be a fixed point")

	for k, want := range src {
		v, ok := again.Get(k)
		require.True(t, ok, "key %q did not survive", k)
		assert.JSONEq(t, string(want), string(v), "key %q", k)
	}
}

func TestSnapshot_MarshalEmptyAndNil(t *testing.T) {
	b, err := json.Marshal(form.Snapshot{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(b))

	b, err = json.Marshal(form.FromMap(nil))
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(b))
}
