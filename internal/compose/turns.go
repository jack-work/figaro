package compose

import (
	"strings"

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
// InquirySegmentsOf is a turn's opening question split by WHO ASKED IT: one
// entry per RUN of consecutive prose blocks sharing a Sender, in message order.
//
// It lives here rather than beside turns.Text because internal/turns
// deliberately imports nothing but the fig IR — turn identity is a property of
// the canonical record, not of any rendering of it — and a segment is an aria
// wire type. compose is the package that already bridges the two.
//
// Returns nil when nothing carries a sender, so a turn recorded before
// attribution existed produces no segments and every renderer falls back to the
// joined text unchanged.
func InquirySegmentsOf(m message.Message) []aria.InquirySegment {
	var out []aria.InquirySegment
	attributed := false
	for _, c := range m.Content {
		if c.Type != message.ContentProse || strings.TrimSpace(c.Text) == "" {
			continue
		}
		if c.Sender != "" {
			attributed = true
		}
		if n := len(out); n > 0 && out[n-1].Sender == c.Sender {
			out[n-1].Text += "\n" + c.Text
			continue
		}
		out = append(out, aria.InquirySegment{Sender: c.Sender, Text: c.Text})
	}
	if !attributed {
		return nil
	}
	return out
}

func Turns(msgs []message.Message, summarize ToolSummary, previewArg ToolPreviewArg) []aria.Turn {
	turns.StampIDs(msgs)
	var out []aria.Turn
	var group []message.Message
	var inquiry string
	var segments []aria.InquirySegment
	var at int64
	var id, first, last uint64

	flush := func() {
		if id == 0 {
			return
		}
		out = append(out, aria.Turn{
			ID: id, Inquiry: inquiry, InquirySegments: segments,
			At: at, LTs: []uint64{first, last}, Sealed: true,
			Nodes: Nodes(group, nil, nil, summarize, previewArg),
		})
		group, id, inquiry, segments, at = nil, 0, "", nil, 0
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
			segments = InquirySegmentsOf(m)
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
