package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	client "github.com/jack-work/figaro/sdk/aria"
)

// A page of history must not touch the open turn.
// ---------------------------------------------------------------------------

// openInquiryPage is what Server.OpenInquiry broadcasts.
func openInquiryPage(id uint64, q string) Page {
	return Page{Parts: []TurnPart{{Turn: Turn{ID: id, Inquiry: q}}}}
}

// historyPage is what ReadBefore returns for a finished aria.
func historyPage(turns ...Turn) Page {
	parts := make([]TurnPart, 0, len(turns))
	for _, t := range turns {
		t.Sealed = true
		parts = append(parts, TurnPart{Turn: t})
	}
	return Page{Parts: parts}
}

func TestApply_HistoryDoesNotClobberTheOpenTurn(t *testing.T) {
	c := client.NewClient()

	c.Apply(openInquiryPage(2, "the new question"), client.Notify)

	if open := c.Open(); open == nil || open.Turn != 2 || open.Inquiry != "the new question" {
		t.Fatalf("precondition: turn 2 must be open with its question, got %+v", open)
	}

	c.Apply(historyPage(Turn{ID: 1, Inquiry: "the old question", Nodes: []livedoc.Node{prose("the old answer")}}), client.Notify)

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

	closed := c.View().Closed
	if len(closed) != 1 || closed[0].Turn != 1 || closed[0].Inquiry != "the old question" {
		t.Fatalf("history was not delivered as one closed message: %+v", closed)
	}
	if len(closed[0].Nodes) != 1 || closed[0].Nodes[0].Markdown != "the old answer" {
		t.Fatalf("history lost its nodes: %+v", closed[0].Nodes)
	}
}

// A sealed historical part with nodes but no inquiry must not claim the open
// slots either.
func TestApply_HistoryWithoutInquiryDoesNotClobberTheOpenTurn(t *testing.T) {
	c := client.NewClient()
	c.Apply(openInquiryPage(5, "still mine"), client.Notify)

	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 3, Sealed: true, Nodes: []livedoc.Node{prose("older")}},
	}}}, client.Notify)

	open := c.Open()
	if open == nil || open.Turn != 5 || open.Inquiry != "still mine" {
		t.Fatalf("a sealed historical part displaced the open turn: %+v", open)
	}
}

// A turn older than the one currently open must never claim it, even unsealed.
func TestApply_OlderTurnNeverClaimsTheOpenSlots(t *testing.T) {
	c := client.NewClient()
	c.Apply(openInquiryPage(9, "current"), client.Notify)
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Inquiry: "stale"}}}}, client.Notify)

	if open := c.Open(); open == nil || open.Turn != 9 {
		t.Fatalf("an older turn claimed the open slots: %+v", open)
	}
}

// A catch-up read that arrives while a turn is streaming still has to open
// it: the last part's Live is what entitles it to the open slots.
func TestApply_CatchUpJoinsARunningTurnMidFlight(t *testing.T) {
	c := client.NewClient()

	c.Apply(Page{Parts: []TurnPart{
		{Turn: Turn{ID: 1, Inquiry: "old", Sealed: true, Nodes: []livedoc.Node{prose("old answer")}}},
		{Turn: Turn{ID: 2, Inquiry: "live one", Nodes: []livedoc.Node{prose("partial")},
			Live: &Live{From: 0, V: 3}}},
	}}, client.Notify)

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
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 2, Live: &Live{From: 0, V: 4, Nodes: []NodeDelta{
		{ID: 1, Set: map[string]any{"type": "prose", "markdown": "second"}},
	}}}}}}, client.Notify)
	open = c.Open()
	if len(open.Nodes) != 2 || open.Nodes[1].Markdown != "second" {
		t.Fatalf("a delta after the catch-up did not fold: %+v", open.Nodes)
	}
}

// A sealed turn delivered clipped must still reach the renderer as slices at
// their true offsets.
func TestApply_ClippedSealedHistoryKeepsItsOffsets(t *testing.T) {
	c := client.NewClient()
	nodes := []livedoc.Node{prose("n0"), prose("n1"), prose("n2"), prose("n3")}

	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Sealed: true, Nodes: nodes[2:]}, From: 2, ClippedHead: true,
	}}}, client.Notify)
	c.Apply(Page{Parts: []TurnPart{{
		Turn: Turn{ID: 7, Inquiry: "q7", Sealed: true, Nodes: nodes[:2]}, From: 0, ClippedTail: true,
	}}}, client.Notify)

	closed := c.View().Closed
	if len(closed) == 0 {
		t.Fatal("clipped sealed history produced no closed messages")
	}
	// The clipped-head slice must not claim to start the turn.
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
