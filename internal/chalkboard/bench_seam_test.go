package chalkboard_test

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// THE SEAM.
//
// Every construction of a chalkboard.Snapshot from a map — and every
// read out of one — in the benchmark suite goes through this file, and
// only this file. On main, Snapshot is `map[string]json.RawMessage` and
// these are conversions/index expressions. After the persistent-tree
// swap the integrator edits the three bodies below (one line each) and
// the entire benchmark suite compiles and re-runs unchanged:
//
//	buildBoard    -> return chalkboard.FromMap(m)
//	boardGet      -> return s.Get(key)
//	boardLen      -> return s.Len()
//
// Do NOT write `chalkboard.Snapshot{...}` literals, `make(chalkboard.
// Snapshot, n)`, `s[k]`, `len(s)` or `range s` anywhere else in the
// benchmark files. That is the whole contract.

// buildBoard constructs a Snapshot from a plain map.
func buildBoard(m map[string]json.RawMessage) chalkboard.Snapshot {
	return chalkboard.Snapshot(m) // AFTER: chalkboard.FromMap(m)
}

// boardGet reads one key.
func boardGet(s chalkboard.Snapshot, key string) (json.RawMessage, bool) {
	v, ok := s[key] // AFTER: return s.Get(key)
	return v, ok
}

// boardLen reports the number of keys.
func boardLen(s chalkboard.Snapshot) int {
	return len(s) // AFTER: return s.Len()
}

// unmarshalBoard decodes a flat `{"key": value, ...}` object into a
// Snapshot. Valid before and after the swap (the swap adds a hand-
// written UnmarshalJSON); routed here so there is one place to look.
func unmarshalBoard(data []byte) (chalkboard.Snapshot, error) {
	var s chalkboard.Snapshot
	err := json.Unmarshal(data, &s)
	return s, err
}
