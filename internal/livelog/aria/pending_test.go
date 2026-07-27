package aria

import (
	"time"

	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// THE INVISIBLE STEER, at the level of the model.
//
// A prompt sent to a BUSY figaro is classified by the DRAIN as a steer, and
// the daemon's steering branch RETURNS BEFORE OpenInquiry — so submitting
// broadcasts nothing at all, and the text first reaches a client when the
// projection emits a steering node at the next ROUND BOUNDARY. Behind a
// forty-second tool call that is forty seconds in which the prompt is
// accepted, real, and on no screen.
//
// Submit is the state that covers it: SUBMITTED, no coordinate. This pins that
// it survives every frame that is not its own ack, and that the ack — whichever
// of the two it turns out to be — takes it down.
func TestClient_SubmittedPromptEchoesUntilTheDrainPlacesIt(t *testing.T) {
	c := NewClient()
	c.Submit("SECONDMESSAGE please acknowledge")

	// The turn we joined streams a tool for a while. None of this is our
	// prompt, and none of it may resolve the echo.
	for i := range 3 {
		c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 7, Nodes: []livedoc.Node{
			{ID: "t1", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusRunning,
				Output: string(rune('a' + i))},
		}, Live: &Live{From: 0, V: i + 1, Nodes: []NodeDelta{{ID: 0,
			Set: map[string]any{"status": livedoc.StatusRunning}}}}}}}})
		if got := c.PendingLen(); got != 1 {
			t.Fatalf("after frame %d the echo count is %d, want 1 — a frame that is not "+
				"our prompt's ack must not take it down", i, got)
		}
	}

	// The round boundary: the drain's steer reaches the projection and the
	// node appears inside the open turn.
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 7, Nodes: []livedoc.Node{
		{ID: "t1", Type: livedoc.NodeTool, Name: "bash", Status: livedoc.StatusOK},
		{ID: "s1", Type: livedoc.NodeSteering, Role: livedoc.RoleInput,
			Markdown: "SECONDMESSAGE please acknowledge"},
	}, Live: &Live{From: 0, V: 9}}}}})

	if got := c.PendingLen(); got != 0 {
		t.Fatalf("the echo count is %d after the steer arrived, want 0 — the prompt now has "+
			"a coordinate, so drawing it twice is the other failure", got)
	}
}

// The OTHER resolution, and the client may not guess between them: our prompt
// opened a turn, so it comes back as the turn's INQUIRY rather than as a node
// inside somebody else's turn.
func TestClient_SubmittedPromptResolvesAsAnInquiry(t *testing.T) {
	c := NewClient()
	c.Submit("what is the gap?")

	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Inquiry: "what is the gap?"}}}})

	if got := c.PendingLen(); got != 0 {
		t.Fatalf("the echo count is %d, want 0 — the prompt acquired a turn id, which is "+
			"the ack for the call-response case", got)
	}
}

// THE ACK LANDS BEFORE THE CALLBACKS FIRE, which is what makes the transition
// one repaint rather than two. A renderer that saw the steering node arrive
// while the echo was still up would draw the text twice for one frame; one
// that saw the echo vanish first would blink.
func TestClient_EchoIsGoneByTheTimeTheFrameIsDrawn(t *testing.T) {
	c := NewClient()
	c.Submit("nudge")

	seen := -1
	c.OnLive = func(Message) { seen = c.PendingLen() }
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 3, Nodes: []livedoc.Node{
		{ID: "s1", Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "nudge"},
	}, Live: &Live{From: 0, V: 1}}}}})

	if seen != 0 {
		t.Fatalf("the renderer saw %d echoes on the frame that delivers the steer, want 0 — "+
			"the ack must resolve INSIDE Apply, or the prompt is on screen twice", seen)
	}
}

// THE DRAIN JOINS A BATCH. Several prompts queued during one tool round become
// ONE message whose prose is the texts joined by newlines, so an echo comes
// back as a contiguous run of lines inside a bigger node. Equality on the text
// would leave every batched steer echoing forever.
func TestClient_ABatchedSteerAcksEveryEchoItCarries(t *testing.T) {
	c := NewClient()
	c.Submit("nudge one")
	c.Submit("nudge two")

	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 4, Nodes: []livedoc.Node{
		{ID: "s1", Type: livedoc.NodeSteering, Role: livedoc.RoleInput,
			Markdown: "nudge one\nnudge two"},
	}, Live: &Live{From: 0, V: 1}}}}})

	if got := c.PendingLen(); got != 0 {
		t.Fatalf("%d echoes survived the batched steer, want 0 — the drain joins a batch "+
			"with newlines, so the ack is containment, not equality", got)
	}
}

// HISTORY CANNOT ACK AN ECHO. A backward read is full of old steers, and one of
// them saying the same words as the prompt we just sent is a coincidence, not a
// classification. The minTurn floor is what refuses it.
func TestClient_HistoryDoesNotAckAnEcho(t *testing.T) {
	c := NewClient()
	// Seal turn 5 so the client has a committed cursor to floor against.
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 5, Sealed: true, Nodes: []livedoc.Node{
		{ID: "p", Type: livedoc.NodeProse, Markdown: "older"},
	}}}}})
	c.Submit("run the tests")

	// A catch-up page of OLD history that happens to contain the same words.
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 2, Sealed: true, Nodes: []livedoc.Node{
		{ID: "s0", Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "run the tests"},
	}}}}})

	if got := c.PendingLen(); got != 1 {
		t.Fatalf("the echo count is %d after a history page, want 1 — a turn at or below the "+
			"submit point cannot be our prompt's coordinate", got)
	}
}

// A page fetched by the pager goes through Merge, which is the SILENT door: no
// OnClosed, no re-freeze — and no ack either, for the same reason history
// cannot ack. Merge is how scroll-up loads a thousand old turns.
func TestClient_MergeDoesNotAckAnEcho(t *testing.T) {
	c := NewClient()
	c.Submit("run the tests")
	c.Merge([]Message{{Turn: 2, From: 0, Role: livedoc.RoleOutput, Nodes: []livedoc.Node{
		{ID: "s0", Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "run the tests"},
	}}}, nil)

	if got := c.PendingLen(); got != 1 {
		t.Fatalf("the echo count is %d after a merged page, want 1", got)
	}
}

// The echoes are ordered oldest-first and carry the text verbatim: the surface
// draws them in the order they were typed, which is the order the drain will
// place them in.
func TestStore_PendingIsFIFO(t *testing.T) {
	s := NewStore()
	s.AddPending("one", time.Now(), 0)
	s.AddPending("two", time.Now(), 0)
	got := s.Pending()
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "two" {
		t.Fatalf("pending = %+v, want [one two] in submit order", got)
	}
	if got[0].ID == got[1].ID {
		t.Fatalf("two echoes share id %d — the renderer keys its rows on it", got[0].ID)
	}
	// Duplicated text acks the OLDEST first, so two identical prompts do not
	// collapse into one echo that never clears.
	s2 := NewStore()
	s2.AddPending("ok", time.Now(), 0)
	s2.AddPending("ok", time.Now(), 0)
	if s2.ResolvePending("ok", 1) != 1 || s2.PendingLen() != 1 {
		t.Fatalf("resolving one of two identical echoes left %d", s2.PendingLen())
	}
}

func TestCarriesLines(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"nudge one\nnudge two", "nudge one", true},
		{"nudge one\nnudge two", "nudge two", true},
		{"a\nb\nc", "b\nc", true},
		{"a\nb\nc", "a\nc", false}, // not contiguous
		{"nudge one", "nudge", false},
		{"  spaced  ", "spaced", true},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := carriesLines(c.got, c.want); got != c.ok {
			t.Errorf("carriesLines(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}
