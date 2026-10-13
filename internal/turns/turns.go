// Package turns is the fig IR turn arithmetic.
package turns

import (
	"strings"

	"github.com/jack-work/figaro/api/message"
)

func Opens(m message.Message) bool {
	return m.Role == message.RoleInput && !IsSteering(m) && Text(m) != ""
}

// IsSteering accepts two shapes: the explicit flag, and the legacy shape of
// prose riding on a tool_result message, which logs on disk still carry.
func IsSteering(m message.Message) bool {
	return m.Steering || HasToolResult(m)
}

// StampIDs fills TurnID where missing and returns the last turn id, the seed
// an agent resumes minting from.
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

// Span: first is the prompt's LT, the coordinate a fork takes.
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

// At reports which turn owns an LT.
func At(msgs []message.Message, lt uint64) (uint64, bool) {
	StampIDs(msgs)
	for i := range msgs {
		if msgs[i].LogicalTime == lt {
			return msgs[i].TurnID, msgs[i].TurnID != 0
		}
	}
	return 0, false
}

// Text joins a message's prose blocks, skipping empty ones.
func Text(m message.Message) string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func HasToolResult(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}
