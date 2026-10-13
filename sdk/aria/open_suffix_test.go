package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// Open() and View() hand back the open suffix only; nodes below openFrom were
// already released as closed messages.
func TestOpenAndViewCarryOnlyTheSuffix(t *testing.T) {
	c := NewClient()

	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Live: &Live{From: 0, Nodes: []NodeDelta{
		{ID: 0, Set: map[string]any{"type": "prose", "role": livedoc.RoleInput, "markdown": "the question"}},
	}}}}}}, Notify)
	c.Apply(Page{Parts: []TurnPart{{Turn: Turn{ID: 1, Live: &Live{From: 1, Nodes: []NodeDelta{
		{ID: 1, Set: map[string]any{"type": "prose", "role": livedoc.RoleOutput, "markdown": "the answer"}},
	}}}}}}, Notify)

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
		if got.Role != livedoc.RoleOutput {
			t.Errorf("%s: role = %q, want output", name, got.Role)
		}
	}

	closed := c.View().Closed
	if len(closed) != 1 || closed[0].Nodes[0].Markdown != "the question" {
		t.Fatalf("the prompt must remain exactly once in the closed set, got %+v", closed)
	}
}
