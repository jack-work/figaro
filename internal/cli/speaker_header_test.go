package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func headerComposer() ldrender.Composer {
	return ldrender.Composer{
		View:        pagerView(&ariaView{}),
		Header:      messageHeader,
		Rule:        func() string { return strings.Repeat("-", 10) },
		InputHeader: inputHeader,
	}
}

func toolNode(id, name, out string) livedoc.Node {
	return livedoc.Node{
		Type: livedoc.NodeTool, Role: livedoc.RoleOutput,
		ID: id, Name: name, Status: livedoc.StatusOK, Output: out,
	}
}

// Chrome rows only: a tool's own output may mention figaro.
func hasHeader(rows []ldrender.Row) bool {
	for _, r := range rows {
		if r.Block == ldrender.BlockChrome && strings.Contains(r.Text, "< figaro") {
			return true
		}
	}
	return false
}

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

// The header belongs to the inquiry seam; a continuation slice has none.
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

func TestSpeakerHeader_ClippedPagePartProducesHeaderlessSlice(t *testing.T) {
	nodes := make([]livedoc.Node, 31)
	for i := range nodes {
		nodes[i] = toolNode("n", "bash", "x")
	}
	page := aria.Page{Parts: []aria.TurnPart{{
		Turn:        aria.Turn{ID: 8, Inquiry: "mint a bunch of new arias", Sealed: true, Nodes: nodes[21:]},
		From:        21,
		ClippedHead: true,
	}}}
	msgs := pageMessages(page)
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

func TestSpeakerHeader_OversizeTurnSliceProducesHeaderlessSlice(t *testing.T) {
	big := strings.Repeat("x", 30000)
	nodes := []livedoc.Node{
		toolNode("a", "bash", big),
		toolNode("b", "bash", big),
		toolNode("c", "bash", "tail"),
	}
	msgs := pageMessages(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 8, Sealed: true, Nodes: nodes},
	}}})
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
