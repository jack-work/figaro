package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// The composer the pager builds (transcript.composer), minus the pager state
// the header decision does not depend on.
func headerComposer() ldrender.Composer {
	return ldrender.Composer{
		View:   pagerView(&ariaView{}),
		Header: messageHeader,
		Rule:   func() string { return strings.Repeat("-", 10) },
		Sender: dimSender,
	}
}

func toolNode(id, name, out string) livedoc.Node {
	return livedoc.Node{
		Type: livedoc.NodeTool, Role: livedoc.RoleOutput,
		ID: id, Name: name, Status: livedoc.StatusOK, Output: out,
	}
}

// A header is CHROME (it belongs to no block) and reads "< figaro". Matching
// on the substring alone is not enough: a tool node's own output may well
// mention figaro, and one of these probes runs `figaro set` on purpose.
func hasHeader(rows []ldrender.Row) bool {
	for _, r := range rows {
		if r.Block == ldrender.BlockChrome && strings.Contains(r.Text, "< figaro") {
			return true
		}
	}
	return false
}

// A slice that STARTS a turn carries the question, and the header belongs
// there. This is the control.
func TestSpeakerHeader_FirstSliceIsCorrect(t *testing.T) {
	m := aria.Message{
		Turn: 8, From: 0, Role: livedoc.RoleOutput,
		Inquiry: "mint a bunch of new arias",
		Nodes:   []livedoc.Node{toolNode("a", "bash", "ok")},
	}
	if !hasHeader(headerComposer().Message(m, 80)) {
		t.Fatalf("first slice should carry the header")
	}
}

// A CONTINUATION slice of the same turn carries no question, so it must not
// announce the speaker again: the header belongs to the inquiry seam, and a
// continuation has no seam. This is the reported bug: a "< figaro" printed
// between two tool nodes of one turn, at the page boundary that cut it.
func TestSpeakerHeader_ContinuationCarriesNone(t *testing.T) {
	m := aria.Message{
		Turn: 8, From: 21, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{toolNode("b", "bash", "figaro set --id 07fd7beb system.model claude-fable-5")},
	}
	rows := headerComposer().Message(m, 80)
	if hasHeader(rows) {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString("\t" + r.Text + "\n")
		}
		t.Fatalf("continuation slice (From=21, no inquiry) drew a speaker header:\n%s", b.String())
	}
}

// The two producers of continuation slices, proven to produce From > 0.

//  1. A page window that opens mid-turn: assemble() sets From=first,
//     ClippedHead=true, and committedMessages drops the inquiry for it.
func TestSpeakerHeader_ClippedPagePartProducesHeaderlessSlice(t *testing.T) {
	nodes := make([]livedoc.Node, 31)
	for i := range nodes {
		nodes[i] = toolNode("n", "bash", "x")
	}
	page := aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 8, Inquiry: "mint a bunch of new arias", Nodes: nodes[21:]},
		From:        21,
		ClippedHead: true,
	}}}
	msgs := committedMessages(page)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].From != 21 || msgs[0].Inquiry != "" {
		t.Fatalf("want From=21 with no inquiry, got From=%d inquiry=%q", msgs[0].From, msgs[0].Inquiry)
	}
	if hasHeader(headerComposer().Message(msgs[0], 80)) {
		t.Fatalf("clipped page part rendered a speaker header with no question above it")
	}
}

//  2. A turn over transcriptUnitChars, cut by appendTurnSlices. Every unit
//     after the first has From > 0 and no inquiry.
func TestSpeakerHeader_OversizeTurnSliceProducesHeaderlessSlice(t *testing.T) {
	big := strings.Repeat("x", transcriptUnitChars/2+1)
	nodes := []livedoc.Node{
		toolNode("a", "bash", big),
		toolNode("b", "bash", big),
		toolNode("c", "bash", "tail"),
	}
	msgs := sliceTurn(8, 0, nodes)
	if len(msgs) < 2 {
		t.Fatalf("expected the turn to be cut, got %d unit(s)", len(msgs))
	}
	for _, m := range msgs[1:] {
		if m.From == 0 {
			t.Fatalf("continuation unit has From=0")
		}
		if hasHeader(headerComposer().Message(m, 80)) {
			t.Fatalf("continuation unit (From=%d) rendered a speaker header", m.From)
		}
	}
}
