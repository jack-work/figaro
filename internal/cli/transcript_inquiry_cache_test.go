package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// A SLICE CAN BE RENDERED BEFORE ITS QUESTION ARRIVES.
//
// The inquiry is TEXT ON THE TURN, delivered on a part of its own, so a slice
// can be materialized and cached before it lands. rowCache is keyed on
// (turn, from) — which does not move when the question shows up — so the first,
// question-less render used to win forever. The turn then displayed a reply
// with nothing above it until something unrelated dropped the cache, which is
// why paging away and back appeared to repair it.
func TestRowCacheRerendersWhenTheInquiryArrivesLate(t *testing.T) {
	const q = "THEQUESTIONARRIVEDLATE"
	nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "THEANSWER"}}

	client := aria.NewClient()
	ft := ldrender.NewFakeTerminal(60, 24)
	tr := newTranscript(ft, 60, 24, &ariaView{settings: &renderSettings{}}, client, "aria1234", time.Unix(0, 0))

	// The turn OPENS with nodes and no question yet — the order the wire
	// actually produces when the agent speaks before the inquiry part lands.
	client.Apply(aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: 1, Nodes: nodes,
			Live: &aria.Live{From: 1, V: 1}},
	}}})
	tr.enter()
	if got := strings.Join(tr.lines(), "\n"); strings.Contains(got, q) {
		t.Fatal("fixture broken: the question is present before it was sent")
	}
	if got := strings.Join(tr.lines(), "\n"); !strings.Contains(got, "THEANSWER") {
		t.Fatalf("fixture broken: the reply never rendered\n%s", got)
	}

	// The question lands for the same turn, then the turn seals.
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: q}},
	}})
	client.Apply(aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 1, Inquiry: q, Sealed: true, Nodes: nodes}},
	}})
	tr.invalidateWindow()

	if got := strings.Join(tr.lines(), "\n"); !strings.Contains(got, q) {
		t.Fatalf("the question never appeared: rowCache served a render made "+
			"before it arrived.\n--- lines ---\n%s", got)
	}
}
