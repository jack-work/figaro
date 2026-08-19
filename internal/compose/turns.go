package compose

import (
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/turns"
)

// Turns projects a message log into turns: the single projection. There is no
// longer a separate "prompt unit": a turn is one exchange, its opening question
// is Turn.Inquiry (text, not a node), and its Nodes are what the agent produced
// plus any steering that rode along.
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

func Turns(msgs []message.Message) []aria.Turn {
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
			Nodes: Nodes(group, nil, nil),
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
