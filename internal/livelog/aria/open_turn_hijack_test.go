package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// ---------------------------------------------------------------------------
// A page of HISTORY must not touch the open turn.
//
// Apply used openTurn/openNodesSlice as ONE staging buffer for every part it
// walked, including sealed history. So a historical part could claim the open
// slots (the inquiry branch and the nodes branch both did
// `if c.openTurn != id { c.resetOpen(); c.openTurn = id }`) and then the sealed
// branch, seeing `c.openTurn == id`, called resetOpen() and threw the LIVE turn
// away.
//
// The user-visible bug: submit to an existing, not-running aria and the
// question paints in incipit; press ^T within the first moment and it VANISHES,
// because enterTranscript does a catch-up ReadBefore whose page is all sealed
// history. It comes back the instant the model's first node streams, because
// c.inquiry[id] survives and the next live frame re-opens the turn. That
// self-heal is why it looks intermittent and why it lasts exactly as long as
// the model thinks.
//
// It needs prior history to reproduce: seenClosed(historical) is false
// precisely because this is a fresh process attaching to an existing aria.
// ---------------------------------------------------------------------------

// openInquiryPage is what Server.OpenInquiry broadcasts: the question alone, no
// nodes, no Live, not sealed.
func openInquiryPage(id uint64, q string) Page {
	return Page{Parts: []TurnPart{{Turn: Turn{ID: id, Inquiry: q}}}}
}

// historyPage is what ReadBefore(recentCursor) returns for a finished aria:
// whole sealed turns, each carrying its own inquiry and nodes.
func historyPage(turns ...Turn) Page {
	parts := make([]TurnPart, 0, len(turns))
	for _, t := range turns {
		t.Sealed = true
		parts = append(parts, TurnPart{Turn: t})
	}
	return Page{Parts: parts}
}

func TestApply_HistoryDoesNotClobberTheOpenTurn(t *testing.T) {
	c := NewClient()

	// Turn 1 finished in an earlier process; we have never seen it.
	// Turn 2 is submitted now: the question commits before the model speaks.
	c.Apply(openInquiryPage(2, "the new question"))

	if open := c.Open(); open == nil || open.Turn != 2 || open.Inquiry != "the new question" {
		t.Fatalf("precondition: turn 2 must be open with its question, got %+v", open)
	}

	// ^T -> enterTranscript -> ReadBefore(recentCursor) -> a page of history.
	c.Apply(historyPage(Turn{ID: 1, Inquiry: "the old question", Nodes: []livedoc.Node{prose("the old answer")}}))

	open := c.Open()
	if open == nil {
		t.Fatal("applying a page of sealed history CLOSED the open turn; the question vanishes from the pager")
	}
	if open.Turn != 2 {
		t.Fatalf("the open turn is now %d, want 2: history claimed the open slots", open.Turn)
	}
	if open.Inquiry != "the new question" {
		t.Fatalf("open inquiry = %q, want the new question", open.Inquiry)
	}

	// And the history itself must still have been delivered, exactly once.
	closed := c.View().Closed
	if len(closed) != 1 || closed[0].Turn != 1 || closed[0].Inquiry != "the old question" {
		t.Fatalf("history was not delivered as one closed message: %+v", closed)
	}
	if len(closed[0].Nodes) != 1 || closed[0].Nodes[0].Markdown != "the old answer" {
		t.Fatalf("history lost its nodes: %+v", closed[0].Nodes)
	}
}

// The same hijack via the NODES branch alone: a sealed historical part with
// nodes but no inquiry (a turn whose question was clipped away) must not claim
// the open slots either.
func TestApply_HistoryWithoutInquiryDoesNotClobberTheOpenTurn(t *testing.T) {
	c := NewClient()
	c.Apply(openInquiryPage(5, "still mine"))

	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 3, Sealed: true, Nodes: []livedoc.Node{prose("older")}},
	}}})

	open := c.Open()
	if open == nil || open.Turn != 5 || open.Inquiry != "still mine" {
		t.Fatalf("a sealed historical part displaced the open turn: %+v", open)
	}
}

// A turn OLDER than the one currently open must never claim it, even unsealed.
func TestApply_OlderTurnNeverClaimsTheOpenSlots(t *testing.T) {
	c := NewClient()
	c.Apply(openInquiryPage(9, "current"))
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "stale"}}}})

	if open := c.Open(); open == nil || open.Turn != 9 {
		t.Fatalf("an older turn claimed the open slots: %+v", open)
	}
}

// THE GUARD THAT MUST NOT BREAK: a catch-up read that legitimately arrives
// while a turn is streaming still has to OPEN it. That page's last part carries
// Live: that is what entitles it to the open slots, and the sealed parts
// before it are history.
func TestApply_CatchUpJoinsARunningTurnMidFlight(t *testing.T) {
	c := NewClient()

	// A fresh viewer: nothing open, and a read that spans history plus the
	// turn currently in flight.
	c.Apply(Page{Parts: []TurnPart{
		{Turn: Turn{ID: 1, Inquiry: "old", Sealed: true, Nodes: []livedoc.Node{prose("old answer")}}},
		{Turn: Turn{ID: 2, Inquiry: "live one", Nodes: []livedoc.Node{prose("partial")},
			Live: &Live{From: 0, V: 3}}},
	}})

	open := c.Open()
	if open == nil {
		t.Fatal("a catch-up read spanning a running turn must open it")
	}
	if open.Turn != 2 || open.Inquiry != "live one" {
		t.Fatalf("open = %+v, want turn 2 carrying its inquiry", open)
	}
	if len(open.Nodes) != 1 || open.Nodes[0].Markdown != "partial" {
		t.Fatalf("the in-flight turn lost its committed head: %+v", open.Nodes)
	}
	// Subsequent deltas must still fold onto it at the right positional ids.
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 2, Live: &Live{From: 0, V: 4, Nodes: []NodeDelta{
		{ID: 1, Set: map[string]any{"type": "prose", "markdown": "second"}},
	}}}}}})
	open = c.Open()
	if len(open.Nodes) != 2 || open.Nodes[1].Markdown != "second" {
		t.Fatalf("a delta after the catch-up did not fold: %+v", open.Nodes)
	}
}

// A sealed turn delivered CLIPPED (too big for one page) must still reach the
// renderer as slices at their true offsets. This is the case that genuinely
// used the staging buffer, so it is the one a "history never stages" fix can
// break.
func TestApply_ClippedSealedHistoryKeepsItsOffsets(t *testing.T) {
	c := NewClient()
	nodes := []livedoc.Node{prose("n0"), prose("n1"), prose("n2"), prose("n3")}

	// Tail half first (as a backward read delivers it), then the head.
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Sealed: true, Nodes: nodes[2:]}, From: 2, ClippedHead: true,
	}}})
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Inquiry: "q7", Sealed: true, Nodes: nodes[:2]}, From: 0, ClippedTail: true,
	}}})

	closed := c.View().Closed
	if len(closed) == 0 {
		t.Fatal("clipped sealed history produced no closed messages")
	}
	// The clipped-head slice must NOT claim to start the turn, or the renderers
	// draw the question above a slice that does not carry one.
	for _, m := range closed {
		if m.From == 2 && m.Inquiry != "" {
			t.Errorf("a clipped-head slice carried the inquiry: %+v", m)
		}
		if m.From != 0 && m.From != 2 {
			t.Errorf("slice at unexpected offset %d: %+v", m.From, m)
		}
		for i, n := range m.Nodes {
			want := nodes[int(m.From)+i].Markdown
			if n.Markdown != want {
				t.Errorf("slice From=%d node %d = %q, want %q", m.From, i, n.Markdown, want)
			}
		}
	}
}
