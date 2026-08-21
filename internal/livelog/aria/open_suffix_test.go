package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// Apply's push path already states the rule: "The live region is the OPEN
// SUFFIX only. Nodes below openFrom were already released as closed messages
// above; redrawing them here would print them twice and wrap the prompt in the
// agent's header."
//
// Open() and View() disobeyed it, handing back the whole node slice. The
// transcript asks Open() every frame, so the pager printed the prompt TWICE -
// once under its own voice and again under the agent's, because turnRole of the
// whole slice is the agent. That is a user-visible duplication of the question
// they just asked.
func TestOpenAndViewCarryOnlyTheSuffix(t *testing.T) {
	c := NewClient()

	// The prompt arrives and closes; the reply opens above it.
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Live: &Live{From: 0, Nodes: []NodeDelta{
		{ID: 0, Set: map[string]any{"type": "prose", "role": livedoc.RoleInput, "markdown": "the question"}},
	}}}}}})
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Live: &Live{From: 1, Nodes: []NodeDelta{
		{ID: 1, Set: map[string]any{"type": "prose", "role": livedoc.RoleOutput, "markdown": "the answer"}},
	}}}}}})

	for name, got := range map[string]*Message{"Open": c.Open(), "View": c.View().Open} {
		if got == nil {
			t.Fatalf("%s: no open message", name)
		}
		if got.From != 1 {
			t.Errorf("%s: From = %d, want 1 (the suffix offset)", name, got.From)
		}
		if len(got.Nodes) != 1 {
			t.Fatalf("%s: %d nodes, want only the suffix: the prompt already closed",
				name, len(got.Nodes))
		}
		if got.Nodes[0].Markdown != "the answer" {
			t.Errorf("%s: suffix node = %q, want the answer", name, got.Nodes[0].Markdown)
		}
		// turnRole over the whole slice reports the agent, which is how the
		// user's own question ended up under "< figaro".
		if got.Role != livedoc.RoleOutput {
			t.Errorf("%s: role = %q, want output", name, got.Role)
		}
	}

	// And the prompt is still there exactly once, as a closed message.
	closed := c.View().Closed
	if len(closed) != 1 || closed[0].Nodes[0].Markdown != "the question" {
		t.Fatalf("the prompt must remain exactly once in the closed set, got %+v", closed)
	}
}
