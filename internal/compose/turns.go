package compose

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

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

// TurnSpan reports the LT range a turn occupies: first is the prompt's LT —
// the coordinate a fork takes — and last is the turn's final message.
//
// This is THE resolver. `fig send <aria>:<turn>` and `fig fork <aria>:<turn>`
// both route through it, and neither re-derives the walk: turn ids are
// ordinals, so resolving one to an LT means counting turn openings, which is
// exactly what StampTurnIDs already does. Deriving it twice is the class of
// bug this whole change exists to kill.
//
// Fork semantics rest on first: atMainLT is exclusive of the frozen prefix
// (prefix [First,atMainLT), branch [atMainLT,Last]), so forking at a turn
// shares everything strictly before the question and replaces the question and
// everything downstream. Because that boundary is always a user prompt, the
// history it freezes always terminates on a complete assistant message — no
// tool_invoke is ever left dangling, so interrupted-tool synthesis is
// unreachable for a user-initiated fork.
func TurnSpan(msgs []message.Message, turn uint64) (first, last uint64, ok bool) {
	StampTurnIDs(msgs)
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

// Turns projects a message log into turns — the single projection. There is no
// longer a separate "prompt unit": a turn is one exchange, and the user's
// question is node 0 of it.
//
// Ordering inside a turn follows the fig IR: the prompt, then each assistant
// block in order, with a tool node carrying both of its coordinates (invoke and
// result). A steering interjection shares its LT with the tool_result it rode
// in on, and lands after the tool nodes — tool [61,62] before steering [62].
//
// Pure over the slice: nothing depends on whether the tail is still open, so
// the streaming projection and the sealed projection cannot disagree.
func Turns(msgs []message.Message, summarize ToolSummary, previewArg ToolPreviewArg) []aria.Turn {
	StampTurnIDs(msgs)
	var turns []aria.Turn
	var group []message.Message
	var prompt []livedoc.Node
	var id, first, last uint64

	flush := func() {
		if id == 0 {
			return
		}
		nodes := append(prompt, Nodes(group, nil, nil, summarize, previewArg)...)
		turns = append(turns, aria.Turn{ID: id, LTs: []uint64{first, last}, Sealed: true, Nodes: nodes})
		group, prompt, id = nil, nil, 0
	}

	for _, m := range msgs {
		if OpensTurn(m) {
			flush()
			id, first = m.TurnID, m.LogicalTime
			for ci, c := range m.Content {
				if c.Type == message.ContentProse {
					prompt = append(prompt, textNode(livedoc.NodeProse, roleUser, m.LogicalTime, ci, c.Text))
				}
			}
		}
		if id == 0 {
			continue // boot / state-only tics precede the first prompt
		}
		last = m.LogicalTime
		group = append(group, m)
	}
	flush()
	return turns
}
