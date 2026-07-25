package compose

import "github.com/jack-work/figaro/internal/message"

// OpensTurn reports whether m begins a new turn. The rule is the one Units
// has always applied implicitly: a user message carrying prose opens a turn,
// but a user message bearing tool_result stays inside the current turn even
// when it also carries text — that text is a steering interjection, not a new
// question.
func OpensTurn(m message.Message) bool {
	return m.Role == message.RoleUser && !hasToolResult(m) && messageText(m) != ""
}

// StampTurnIDs fills TurnID on every message that lacks one and returns the
// last turn id the log implies — the seed an agent resumes minting from.
//
// It is a pure function of the slice: the same messages always yield the same
// ids, and nothing depends on whether the tail is still open. A message that
// already carries a TurnID resynchronises the counter, so a log with a legacy
// prefix and a stamped suffix agrees end to end.
//
// Fork seeding needs no machinery of its own. A child shares its parent's
// prefix verbatim, so counting turn openings over that prefix reproduces the
// parent's ids exactly and the child continues from there — siblings then
// conflict piecewise, which is precisely how LT already behaves.
//
// Messages before the first prompt (boot, state-only tics) belong to no turn
// and keep 0.
func StampTurnIDs(msgs []message.Message) uint64 {
	var cur uint64
	for i := range msgs {
		if OpensTurn(msgs[i]) {
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
