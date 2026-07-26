package compose

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/turns"
)

// Turns projects a message log into turns — the single projection. There is no
// longer a separate "prompt unit": a turn is one exchange, its opening question
// is Turn.Inquiry (text, not a node), and its Nodes are what the agent produced
// plus any steering that rode along.
//
// Ordering inside a turn follows the fig IR: each assistant block in order,
// with a tool node carrying both of its coordinates (invoke and result). A
// steering interjection shares its LT with the tool_result it rode in on, and
// lands after the tool nodes — tool [61,62] before steering [62].
//
// Pure over the slice: nothing depends on whether the tail is still open, so
// the streaming projection and the sealed projection cannot disagree.
//
// The turn ARITHMETIC it rests on (which message opens a turn, what id each
// carries) lives in internal/turns, which knows only the fig IR. This function
// is the only part that needs the UI IR.
func Turns(msgs []message.Message, summarize ToolSummary, previewArg ToolPreviewArg) []aria.Turn {
	turns.StampIDs(msgs)
	var out []aria.Turn
	var group []message.Message
	var inquiry string
	var at int64
	var id, first, last uint64

	flush := func() {
		if id == 0 {
			return
		}
		out = append(out, aria.Turn{
			ID: id, Inquiry: inquiry, At: at, LTs: []uint64{first, last}, Sealed: true,
			Nodes: Nodes(group, nil, nil, summarize, previewArg),
		})
		group, id, inquiry, at = nil, 0, "", 0
	}

	for _, m := range msgs {
		if turns.Opens(m) {
			flush()
			id, first = m.TurnID, m.LogicalTime
			// The inquiry is the whole opening question as text. turns.Opens
			// already required non-empty prose, so an inquiry is 1:1 with a turn
			// boundary by construction: a turn cannot open without one, and a
			// second cannot arrive without closing the first.
			inquiry = turns.Text(m)
			// The inquiry is bare text and cannot carry its own timestamp, so the
			// TURN carries it: At is when the question arrived.
			at = m.Timestamp
		}
		if id == 0 {
			continue // boot / state-only tics precede the first prompt
		}
		last = m.LogicalTime
		group = append(group, m)
	}
	flush()
	return out
}
