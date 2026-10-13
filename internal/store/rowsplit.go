package store

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/store/xwal"
)

// A translator record is a JSON array; its rows are split as windows onto the
// payload rather than decoded, which would copy every element.
//
// Alias on the way to the wire; copy on the way into the cache. An aliased row
// pins the frame it was read from and reports less than it holds, so a cache
// budget summing row lengths would stop bounding what it believes it bounds.
// ScanRange aliases, everything else copies.

// splitJSONArray returns windows onto b; ok is false for anything that is not
// an array, which sends the caller back to encoding/json.
func splitJSONArray(b []byte) ([]json.RawMessage, bool) {
	i := skipJSONSpace(b, 0)
	if i >= len(b) || b[i] != '[' {
		return nil, false
	}
	i = skipJSONSpace(b, i+1)
	if i < len(b) && b[i] == ']' {
		if skipJSONSpace(b, i+1) != len(b) {
			return nil, false
		}
		// An empty array is an empty slice, not nil, as encoding/json answers.
		return []json.RawMessage{}, true
	}
	var out []json.RawMessage
	for {
		start := i
		end, ok := xwal.JSONValueEnd(b, i)
		if !ok || end <= start {
			return nil, false
		}
		// Three-index slice: a row may not grow into its neighbour by append.
		out = append(out, json.RawMessage(b[start:end:end]))
		i = skipJSONSpace(b, end)
		if i >= len(b) {
			return nil, false
		}
		switch b[i] {
		case ',':
			i = skipJSONSpace(b, i+1)
			if i >= len(b) || b[i] == ']' {
				return nil, false // a trailing comma is not our shape
			}
		case ']':
			if skipJSONSpace(b, i+1) != len(b) {
				return nil, false
			}
			return out, true
		default:
			return nil, false
		}
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// aliasRows reports the rows as windows onto payload when T is
// []json.RawMessage; otherwise ok is false and the caller decodes normally.
func aliasRows[T any](payload []byte, v *T) bool {
	rows, ok := any(v).(*[]json.RawMessage)
	if !ok {
		return false
	}
	parts, ok := splitJSONArray(payload)
	if !ok {
		return false
	}
	*rows = parts
	return true
}
