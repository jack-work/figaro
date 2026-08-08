package cli

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// Depicts the wire and the client-side shape of ONE turn delivered as TWO
// pages. Run with -v; it prints, it does not assert.
func TestDepict_TurnSplitAcrossTwoPages(t *testing.T) {
	nodes := make([]livedoc.Node, 5)
	for i := range nodes {
		nodes[i] = livedoc.Node{
			Type: livedoc.NodeTool, Role: livedoc.RoleOutput,
			ID: fmt.Sprintf("call_%d", i), Name: "bash",
			Status: livedoc.StatusOK,
			Output: fmt.Sprintf("output of node %d", i),
			LTs:    []uint64{uint64(100 + i)},
		}
	}
	turns := []aria.Turn{{
		ID: 8, Sealed: true, LTs: []uint64{100, 104},
		Inquiry: "mint a bunch of new arias",
		Nodes:   nodes,
	}}

	// Budget chosen to spend out mid-turn, which is what makes the cut.
	const budget = 380

	older := aria.PaginateBefore(turns, aria.Anchor{Turn: 8, Node: 3}, budget)
	newer := aria.Paginate(turns, aria.Anchor{Turn: 8, Node: 3}, aria.Forward, budget)

	for name, p := range map[string]aria.Page{"PAGE 1 (older half)": older, "PAGE 2 (newer half)": newer} {
		b, _ := json.MarshalIndent(p, "", "  ")
		t.Logf("\n=== %s — WIRE ===\n%s", name, b)
		for _, m := range committedMessages(p) {
			t.Logf("=== %s — CLIENT aria.Message ===\n"+
				"  Turn=%d From=%d Role=%q Inquiry=%q len(Nodes)=%d  sliceKey=%d",
				name, m.Turn, m.From, m.Role, m.Inquiry, len(m.Nodes), keyOf(m))
		}
	}
}
