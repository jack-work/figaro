// Package turns is the fig IR turn arithmetic: which message opens a turn,
// what id each message carries, and how a turn id maps to the LT range it
// occupies.
package turns

import (
	"strings"

	"github.com/jack-work/figaro/api/message"
)

// Opens reports whether m begins a new turn.
func Opens(m message.Message) bool {
	return m.Role == message.RoleInput && !IsSteering(m) && Text(m) != ""
}

// IsSteering reports whether m is a mid-turn direction rather than a new
// question. Two accepted shapes for one concept: the explicit flag the drain
// sets, and the legacy shape: prose riding on a tool_result message: which
// real logs still contain and which must keep rendering as it always did.
func IsSteering(m message.Message) bool {
	return m.Steering || HasToolResult(m)
}

// StampIDs fills TurnID on every message that lacks one and returns the last
// turn id the log implies: the seed an agent resumes minting from.
func StampIDs(msgs []message.Message) uint64 {
	var cur uint64
	for i := range msgs {
		if Opens(msgs[i]) {
			cur++
		}
		if id := msgs[i].TurnID; id != 0 {
			cur = id
			continue
		}
		msgs[i].TurnID = cur
	}
	return cur
}

// Span reports the LT range a turn occupies: first is the prompt's LT: the
// coordinate a fork takes, and last is the turn's final message.
func Span(msgs []message.Message, turn uint64) (first, last uint64, ok bool) {
	StampIDs(msgs)
	for i := range msgs {
		if msgs[i].TurnID != turn {
			continue
		}
		if !ok {
			first, ok = msgs[i].LogicalTime, true
		}
		last = msgs[i].LogicalTime
	}
	return first, last, ok
}

// At is the inverse of Span: it reports which turn owns an LT. Display
// surfaces need this because the tree records a fork point as an LT
// (NodeView.BranchedLT) while the user's coordinate is a turn: printing the
// raw LT and calling it a fork argument is a lie, since `fork <id>:N` takes N
// as a turn.
func At(msgs []message.Message, lt uint64) (uint64, bool) {
	StampIDs(msgs)
	for i := range msgs {
		if msgs[i].LogicalTime == lt {
			return msgs[i].TurnID, msgs[i].TurnID != 0
		}
	}
	return 0, false
}

// Text joins a message's prose blocks. Empty blocks are skipped here because
// this answers "does this message say anything", not "what does it render as".
func Text(m message.Message) string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// HasToolResult reports whether a message carries any tool_result block: i.e.
// it is a tool-result tic (part of the turn) rather than a fresh user prompt.
func HasToolResult(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}
