package form_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/form"
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
	assert.Equal(t, raw(t, "v"), p.Set["k"])
}

// --- JSON wire shape. the form channel on disk, the RPC
// FormResponse, and formReduce in internal/store all
// depend on Snapshot marshalling as a flat object. ---

func TestSnapshot_MarshalsAsFlatObject(t *testing.T) {
	s := form.FromMap(map[string]json.RawMessage{
		"cwd":                 json.RawMessage(`"/home/figaro"`),
		"model":               json.RawMessage(`"claude-opus-4-6"`),
		"count":               json.RawMessage(`42`),
		"flag":                json.RawMessage(`true`),
		"nil":                 json.RawMessage(`null`),
		"nested":              json.RawMessage(`{"b":2,"a":[1,2,{"c":3}]}`),
		"list":                json.RawMessage(`[1,"two",{"three":3}]`),
		"system.credo":        json.RawMessage(`"largo al factotum"`),
		"empty.string":        json.RawMessage(`""`),
		"unicode":             json.RawMessage(`"caffè ☕ \u00e9"`),
		"skills.figaro":       json.RawMessage(`{"filePath":"/x/y.md","frontmatter":"name: figaro"}`),
		"a key with spaces":   json.RawMessage(`"ok"`),
		"quote\"in\\the\nkey": json.RawMessage(`"tricky"`),
	})

	got, err := json.Marshal(s)
	require.NoError(t, err)

	const want = `{` +
		`"a key with spaces":"ok",` +
		`"count":42,` +
		`"cwd":"/home/figaro",` +
		`"empty.string":"",` +
		`"flag":true,` +
		`"list":[1,"two",{"three":3}],` +
		`"model":"claude-opus-4-6",` +
		`"nested":{"b":2,"a":[1,2,{"c":3}]},` +
		`"nil":null,` +
		`"quote\"in\\the\nkey":"tricky",` +
		`"skills.figaro":{"filePath":"/x/y.md","frontmatter":"name: figaro"},` +
		`"system.credo":"largo al factotum",` +
		// RawMessage values pass through verbatim (compacted, never
		// re-escaped): the \u00e9 escape survives as written.
		`"unicode":"caffè ☕ \u00e9"` +
		`}`
	assert.Equal(t, want, string(got), "Snapshot must marshal as a flat object with sorted keys")
}

func TestSnapshot_JSONRoundTripByteIdentical(t *testing.T) {
	// A non-trivial board as it would appear on disk, keys already in
	// the order encoding/json emits.
	const onDisk = `{` +
		`"cwd":"/home/gluck/dev/figaro-qua/main",` +
		`"datetime":"Friday, July 24, 2026, 11PM EDT",` +
		`"label":"morning",` +
		`"mantra":"Give Snapshot accessors, migrate every call site",` +
		`"model":"claude-opus-4-6",` +
		`"skills.docker":{"filePath":"/c/docker.md","frontmatter":"name: docker\ndescription: containers"},` +
		`"skills.figaro":{"filePath":"/c/figaro/SKILL.md","frontmatter":"name: figaro"},` +
		`"system.credo":"Largo al factotum della città!",` +
		`"system.environment.figaro_wire_dir":"/tmp/wire",` +
		`"tokens":{"in":12345,"out":678},` +
		`"trunk":["root","conv"]` +
		`}`

	var s form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(onDisk), &s))
	assert.Equal(t, 11, s.Len())

	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Equal(t, onDisk, string(out), "unmarshal -> marshal must be byte-identical")

	// And again, to prove the round trip is a fixed point.
	var s2 form.Snapshot
	require.NoError(t, json.Unmarshal(out, &s2))
	out2, err := json.Marshal(s2)
	require.NoError(t, err)
	assert.Equal(t, onDisk, string(out2))

	// Values survive verbatim, including nested object key order.
	v, ok := s.Get("skills.docker")
	require.True(t, ok)
	assert.Equal(t, `{"filePath":"/c/docker.md","frontmatter":"name: docker\ndescription: containers"}`, string(v))
}

func TestSnapshot_MarshalEmptyAndNil(t *testing.T) {
	b, err := json.Marshal(form.Snapshot{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(b))

	b, err = json.Marshal(form.FromMap(nil))
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(b))
}
