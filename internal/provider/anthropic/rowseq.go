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

var toolUseToken = []byte(`"tool_use"`)

// pairToolCallsSeq is the wire's pairing law: a tool_result is carried only
// when the message before it made that call, and a call the next message does
// not answer is closed here with an error result.
//
// Either half unpaired is a 400 on EVERY later request, so one bad row on
// disk ends the aria. It happened: a turn cut mid-arguments cached an
// assistant message with no tool_use while the IR kept the call, and the
// closing result the seal wrote had nothing to pair with (aria 90ec6584,
// "messages.144.content.1: unexpected tool_use_id found in tool_result
// blocks"). The capture side no longer produces that pair; this gate is what
// carries the histories already written, and it holds one row and a set of
// open ids, never the history.
//
// ADJACENCY IS A PROPERTY OF THE ASSEMBLED CONVERSATION, NOT OF THE LOG. The
// law says "the message before it", and the IR appends one record per tool
// result: a batch calling X and Y arrives as two user records, so the gate saw
// Y's call answered by nothing, closed it with the unclosed-call notice, and
// then dropped Y's real result as an orphan. That is every parallel tool call,
// not a damaged history. Coalescing first is what makes the law mean what it
// says, and it is the shape the wire wants anyway: one assistant message's
// results belong in the single user message that follows it. The assembled
// body does not move -- the pipeline coalesces after this gate too.
func pairToolCallsSeq(in provider.RowSeq) provider.RowSeq {
	in = coalesceRowsSeq(in)
	return func(yield func(json.RawMessage, uint64) bool) {
		var (
			open    []string // calls the held row made, still unanswered
			held    json.RawMessage
			heldLT  uint64
			have    bool
			stopped bool
		)
		emit := func(row json.RawMessage, lt uint64) bool {
			if !yield(row, lt) {
				stopped = true
				return false
			}
			return true
		}
		// release sends the held row and, directly after it, the results
		// closing whatever it called and nothing answered: the API wants
		// those in the message that follows the call.
		release := func() bool {
			if !have || !emit(held, heldLT) {
				return false
			}
			if len(open) == 0 {
				return true
			}
			closing, err := closingResultsRow(open)
			open = nil
			if err != nil {
				return true
			}
			return emit(closing, heldLT)
		}
		in(func(row json.RawMessage, lt uint64) bool {
			answered, calls, rewritten, skip := pairRow(row, open)
			open = remaining(open, answered)
			if have && !release() {
				return false
			}
			have = false
			if skip {
				return true
			}
			if rewritten != nil {
				row = rewritten
			}
			held, heldLT, have, open = row, lt, true, calls
			return true
		})
		if !stopped {
			release()
		}
	}
}

// pairRow reads one row against the calls still open: which of them it
// answers, which calls it makes, and the row with unpaired results removed.
// rewritten is nil when the row needed no rewrite, and then the bytes on the
// wire are the bytes on disk; skip says the row lost every block it had.
func pairRow(row json.RawMessage, open []string) (answered, calls []string, rewritten json.RawMessage, skip bool) {
	if !bytes.Contains(row, toolResultToken) && !bytes.Contains(row, toolUseToken) {
		return nil, nil, nil, false
	}
	var m nativeMessage
	if json.Unmarshal(row, &m) != nil {
		// A row we cannot read is a row we must not rewrite.
		return nil, nil, nil, false
	}
	keep := make([]nativeBlock, 0, len(m.Content))
	dropped := false
	for _, b := range m.Content {
		switch {
		case b.Type == "tool_result" && b.ToolUseID != "":
			if !contains(open, b.ToolUseID) {
				dropped = true
				continue
			}
			answered = append(answered, b.ToolUseID)
		case b.Type == "tool_use" && b.ID != "":
			calls = append(calls, b.ID)
		}
		keep = append(keep, b)
	}
	if !dropped {
		return answered, calls, nil, false
	}
	if len(keep) == 0 {
		// An emptied message is skipped, and its logical time with it.
		return answered, calls, nil, true
	}
	m.Content = keep
	fixed, err := json.Marshal(m)
	if err != nil {
		return answered, calls, nil, false
	}
	return answered, calls, fixed, false
}

// closingResultsRow is the user message that closes calls nothing answered.
func closingResultsRow(ids []string) (json.RawMessage, error) {
	blocks := make([]nativeBlock, 0, len(ids))
	for _, id := range ids {
		blocks = append(blocks, nativeBlock{
			Type: "tool_result", ToolUseID: id, IsError: true,
			Content: []nativeBlock{{Type: "text", Text: unclosedCallNotice}},
		})
	}
	return json.Marshal(nativeMessage{Role: "user", Content: blocks})
}

const unclosedCallNotice = "tool call was never closed; the turn ended first"

func contains(ids []string, id string) bool {
	for _, s := range ids {
		if s == id {
			return true
		}
	}
	return false
}

func remaining(open, answered []string) []string {
	if len(open) == 0 || len(answered) == 0 {
		return open
	}
	out := open[:0]
	for _, id := range open {
		if !contains(answered, id) {
			out = append(out, id)
		}
	}
	return out
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
