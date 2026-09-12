package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func deltaSet(form string, kv map[string]string) map[string]livedoc.FormDelta {
	out := map[string]livedoc.FormDelta{}
	for k, v := range kv {
		out[form+"."+k] = livedoc.FormDelta{
			Value: json.RawMessage(`"` + v + `"`),
			Kind:  livedoc.FormBound, Event: livedoc.FormSet, Form: form,
		}
	}
	return out
}

// ^N walks the delta table as a unit of its own, immediately after the
// block it explains, and never merges with it.
func TestDeltaTableIsItsOwnSelectionStop(t *testing.T) {
	ft := ldrender.NewFakeTerminal(80, 40)
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "go",
		FormDeltas: deltaSet("a1", map[string]string{"mantra": "one"}),
		Nodes: []livedoc.Node{
			{Type: livedoc.NodeProse, Markdown: "first node", FormDeltas: deltaSet("a1", map[string]string{"phase": "two"})},
			{Type: livedoc.NodeProse, Markdown: "second node"},
		},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 80, 40, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	want := []nodeRef{
		{turn: 1, index: inquiryNode},
		{turn: 1, index: inquiryNode, delta: true},
		{turn: 1, index: 0},
		{turn: 1, index: 0, delta: true},
		{turn: 1, index: 1},
	}
	got := make([]nodeRef, 0, len(want))
	for range want {
		tr.selectNode(1, false)
		got = append(got, tr.selection.focus.nodeRef)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop %d: want %+v, got %+v (all %+v)", i, want[i], got[i], got)
		}
	}
}

// Enter on the table opens it, exactly as it opens a tool node, and the
// node above it stays folded.
func TestEnterExpandsTheDeltaTableAlone(t *testing.T) {
	ft := ldrender.NewFakeTerminal(100, 40)
	client := aria.NewClient()
	deltas := deltaSet("a1", map[string]string{
		"k1": "one", "k2": "two", "k3": "three", "k4": "four", "k5": strings.Repeat("v", 60),
	})
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
		ID: 1, Sealed: true, Inquiry: "go",
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "a node", FormDeltas: deltas}},
	}}}}, aria.Notify)
	tr := newTranscript(ft, 100, 40, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()

	if screen := stripANSI(strings.Join(tr.lines(), "\n")); !strings.Contains(screen, "⋯ +") {
		t.Fatalf("a five row table must draw collapsed with a count:\n%s", screen)
	}
	tr.selectNode(1, false) // the inquiry
	tr.selectNode(1, false) // the node
	tr.selectNode(1, false) // its delta table
	if ref := tr.selection.focus.nodeRef; !ref.delta {
		t.Fatalf("the third stop is the table: %+v", ref)
	}
	tr.key(0x0d) // Enter
	screen := stripANSI(strings.Join(tr.lines(), "\n"))
	if strings.Contains(screen, "⋯ +") {
		t.Fatalf("Enter on the table must show the held-back rows:\n%s", screen)
	}
	if !strings.Contains(screen, strings.Repeat("v", 60)) {
		t.Fatalf("Enter on the table must show its values whole:\n%s", screen)
	}
	if tr.expanded[nodeRef{turn: 1, index: 0}] {
		t.Fatal("expanding the table must not expand the node above it")
	}
}
