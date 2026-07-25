package compose

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/turns"
)

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
//
// The turn ARITHMETIC it rests on (which message opens a turn, what id each
// carries) lives in internal/turns, which knows only the fig IR. This function
// is the only part that needs the UI IR.
func Turns(msgs []message.Message, summarize ToolSummary, previewArg ToolPreviewArg) []aria.Turn {
	turns.StampIDs(msgs)
	var out []aria.Turn
	var group []message.Message
	var prompt []livedoc.Node
	var id, first, last uint64

	flush := func() {
		if id == 0 {
			return
		}
		nodes := append(prompt, Nodes(group, nil, nil, summarize, previewArg)...)
		out = append(out, aria.Turn{ID: id, LTs: []uint64{first, last}, Sealed: true, Nodes: nodes})
		group, prompt, id = nil, nil, 0
	}

	for _, m := range msgs {
		if turns.Opens(m) {
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
	return out
}
