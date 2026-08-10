package form_test

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/form"
)

// THE SEAM.
//
// Every construction of a form.Snapshot from a map — and every
// read out of one — in the benchmark suite goes through this file, and
// only this file. On main, Snapshot is `map[string]json.RawMessage` and
// these are conversions/index expressions. After the persistent-tree
// swap the integrator edits the three bodies below (one line each) and
// the entire benchmark suite compiles and re-runs unchanged:
//
//	buildBoard    -> return form.FromMap(m)
//	boardGet      -> return s.Get(key)
//	boardLen      -> return s.Len()
//
// Do NOT write `form.Snapshot{...}` literals, `make(form.
// Snapshot, n)`, `s[k]`, `len(s)` or `range s` anywhere else in the
// benchmark files. That is the whole contract.

// buildBoard constructs a Snapshot from a plain map.
func buildBoard(m map[string]json.RawMessage) form.Snapshot {
	return form.FromMap(m)
}

// boardGet reads one key.
func boardGet(s form.Snapshot, key string) (json.RawMessage, bool) {
	return s.Get(key)
}

// boardLen reports the number of keys.
func boardLen(s form.Snapshot) int {
	return s.Len()
}

// unmarshalBoard decodes a flat `{"key": value, ...}` object into a
// Snapshot. Valid before and after the swap (the swap adds a hand-
// written UnmarshalJSON); routed here so there is one place to look.
func unmarshalBoard(data []byte) (form.Snapshot, error) {
	var s form.Snapshot
	err := json.Unmarshal(data, &s)
	return s, err
}
