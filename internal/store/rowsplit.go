package store

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/store/xwal"
)

// Rows without the copy.
//
// A translator record holds a JSON ARRAY of wire messages, and decoding it
// with encoding/json costs one allocation and one memcpy PER MESSAGE:
// json.RawMessage.UnmarshalJSON is `append(m[0:0], data...)`, so a 2KB row
// becomes a 2KB copy that lives until the request ends. Multiplied by the
// conversation, that was the last term still linear in history -- roughly
// 2.6KB and 24 allocations per row per send, measured.
//
// The array is already on the heap, whole, in r.Payload. Its elements are
// WINDOWS onto it. Splitting rather than decoding turns N copies into N slice
// headers.
//
// # THE RULE, AND IT IS NOT NEGOTIABLE
//
//	ALIAS ON THE WAY TO THE WIRE. COPY ON THE WAY INTO THE CACHE.
//
// An aliased row is a view into the frame the segment handed back. That is
// safe -- the codec allocates a fresh buffer per read (segment/codec.go:
// `buf := make([]byte, length)`), nothing is mmap'd, nothing is reused, and no
// caller writes through a row -- but it PINS the frame for as long as the row
// lives, and it makes the row's apparent size (len) smaller than the memory it
// holds.
//
// For a row on its way to the request body that is free: it lives for one
// write and is dropped, so nothing is pinned that was not already resident.
//
// For a row on its way INTO THE TREE CACHE it is a lie. The cache sizes an
// entry as the sum of its rows' lengths (transEntrySize), so an entry whose
// rows are windows onto a larger frame reports less than it holds, and the
// budget stops bounding what it believes it is bounding. THAT IS EXACTLY THE
// FAILURE eeec2bc0 WAS WRITTEN ABOUT -- "it held SLICES RETURNED BY THE LOG,
// pinning payloads the window believed it had evicted" -- and re-introducing
// it here, one layer down, would be the same bug wearing a different hat.
//
// So the two decoders are separate functions with separate call sites:
// ScanRange aliases, everything else copies. Enforced by
// TestCacheSeededRowsDoNotAliasTheirFrame.

// splitJSONArray splits a JSON array into its elements WITHOUT COPYING: every
// returned row is a window onto b. ok is false for anything that is not an
// array, which sends the caller back to encoding/json.
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
		// An empty array is an empty slice, not a nil one: encoding/json
		// makes that distinction and this function answers as it does.
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

// aliasRows reports the rows of a payload as windows onto it, for a T that is
// []json.RawMessage and nothing else. ok is false for every other T and for a
// payload the splitter does not recognise, and the caller decodes normally.
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
