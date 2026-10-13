package anthropic

import (
	"bytes"
	"encoding/json"
	"iter"

	"github.com/jack-work/figaro/internal/provider"
)

// The assembler's passes hold one row, never the history.

var toolResultToken = []byte(`"tool_result"`)

// dropDuplicateResultsSeq removes a tool_result whose call an earlier block
// already answered; the wire refuses a second. Rows without the token are
// not decoded.
func dropDuplicateResultsSeq(in provider.RowSeq) provider.RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		seen := map[string]bool{}
		in(func(row json.RawMessage, lt uint64) bool {
			if !bytes.Contains(row, toolResultToken) {
				return yield(row, lt)
			}
			var m nativeMessage
			if json.Unmarshal(row, &m) != nil {
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

// pairToolCallsSeq enforces the wire's pairing law: a tool_result is carried
// only when the message before it made the call, and an unanswered call is
// closed with an error result. Either half unpaired is a 400 on every later
// request. Adjacency is judged on coalesced rows: the IR appends one record
// per result, so a parallel batch arrives as several user records.
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

// pairRow reports which open calls a row answers, which it makes, and the
// row with unpaired results removed (nil when untouched; skip when emptied).
func pairRow(row json.RawMessage, open []string) (answered, calls []string, rewritten json.RawMessage, skip bool) {
	if !bytes.Contains(row, toolResultToken) && !bytes.Contains(row, toolUseToken) {
		return nil, nil, nil, false
	}
	var m nativeMessage
	if json.Unmarshal(row, &m) != nil {
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
		return answered, calls, nil, true
	}
	m.Content = keep
	fixed, err := json.Marshal(m)
	if err != nil {
		return answered, calls, nil, false
	}
	return answered, calls, fixed, false
}

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

// coalesceRowsSeq merges adjacent same-role rows; the later LT wins.
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

// markRowsSeq marks the tail row and the last row of each tagged LT with one
// row of lookahead; rows arrive in non-decreasing LT order. A per-LT tag on
// the tail row overwrites the tail breakpoint.
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
