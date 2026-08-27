package anthropic

import (
	"bytes"
	"encoding/json"
	"iter"

	"github.com/jack-work/figaro/internal/provider"
)

// The assembler's passes, as sequences.
//
// Every one of them used to take []json.RawMessage -- the whole conversation,
// decoded, in the heap, for the length of a request. They are the same passes
// in the same order producing the same bytes; what changed is that a row is
// read, rewritten if it must be, written to the wire and dropped, so the cost
// is one row and not the history. The equality is not argued, it is asserted:
// TestStreamedAssemblyIsByteIdenticalToTheSliceAssembler runs both over the same fixtures and
// compares the bodies byte for byte.

var toolResultToken = []byte(`"tool_result"`)

// dropDuplicateResultsSeq removes a tool_result block whose call an EARLIER
// block already answered. The wire pairs one result with one invoke, so a
// second is refused with "unexpected tool_use_id found in tool_result blocks"
// and the whole history becomes unsendable.
//
// The fig IR is append-only and keeps both records, which is honest: two
// closings really were written. This is the last gate before the wire.
//
// A ROW THAT CANNOT CARRY A tool_result IS NOT DECODED. The slice version
// unmarshalled every row in the conversation to populate `seen`, which is a
// second full decode of the history per send on top of the first; only a row
// holding the literal token can contribute to it, and the scan for the token
// is a substring search over bytes already in hand.
func dropDuplicateResultsSeq(in provider.RowSeq) provider.RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		seen := map[string]bool{}
		in(func(row json.RawMessage, lt uint64) bool {
			if !bytes.Contains(row, toolResultToken) {
				return yield(row, lt)
			}
			var m nativeMessage
			if json.Unmarshal(row, &m) != nil {
				// A row we cannot read is a row we must not rewrite.
				return yield(row, lt)
			}
			keep := make([]nativeBlock, 0, len(m.Content))
			dropped := false
			for _, b := range m.Content {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					if seen[b.ToolUseID] {
						dropped = true
						continue
					}
					seen[b.ToolUseID] = true
				}
				keep = append(keep, b)
			}
			if !dropped {
				return yield(row, lt)
			}
			if len(keep) == 0 {
				// An emptied message is skipped, AND ITS LOGICAL TIME GOES
				// WITH IT. The slice version compacted the rows and left the
				// LT array at its old length, so every per-LT tag after a drop
				// addressed the wrong message. A pair cannot desynchronize.
				return true
			}
			m.Content = keep
			fixed, err := json.Marshal(m)
			if err != nil {
				return yield(row, lt)
			}
			return yield(fixed, lt)
		})
	}
}

// coalesceRowsSeq merges adjacent rows that share a role, holding exactly one
// row back to do it. The later LT wins: it is the one a per-LT tag would
// target, which is what the SDK path does with the same choice.
func coalesceRowsSeq(in provider.RowSeq) provider.RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		var (
			held     json.RawMessage
			heldLT   uint64
			heldRole string
			have     bool
			stopped  bool
		)
		in(func(row json.RawMessage, lt uint64) bool {
			role := rowRole(row)
			if have && role != "" && role == heldRole {
				if merged, ok := mergeRows(held, row); ok {
					held, heldLT = merged, lt
					return true
				}
			}
			if have && !yield(held, heldLT) {
				stopped = true
				return false
			}
			held, heldLT, heldRole, have = row, lt, role, true
			return true
		})
		if !stopped && have {
			yield(held, heldLT)
		}
	}
}

// markRowsSeq attaches cache_control to the rolling tail and to the last row
// of each tagged logical time, WITH A LOOKAHEAD OF ONE ROW.
//
// "The last row carrying LT n" was an index into the assembled array. It does
// not need to be: rows arrive in non-decreasing LT order -- the log is ordered
// and coalescing takes the later LT -- so the last row of an LT is the one
// whose successor carries a different one, and the last row of all is the one
// with no successor. One row of lookahead knows both.
//
// THE ORDER OF THE TWO MARKS IS THE SLICE VERSION'S. The tail breakpoint is
// spent first and a per-LT tag on the same row overwrites it.
func markRowsSeq(in provider.RowSeq, tail *cacheControl, tags map[uint64]*cacheControl) provider.RowSeq {
	if tail == nil && len(tags) == 0 {
		return in
	}
	return func(yield func(json.RawMessage, uint64) bool) {
		next, stop := iter.Pull2(in)
		defer stop()
		row, lt, ok := next()
		for ok {
			nextRow, nextLT, more := next()
			last := !more
			if last && tail != nil {
				markRowTail(&row, tail)
			}
			if lt != 0 && (last || nextLT != lt) {
				if cc := tags[lt]; cc != nil {
					markRowTail(&row, cc)
				}
			}
			if !yield(row, lt) {
				return
			}
			row, lt, ok = nextRow, nextLT, more
		}
	}
}
