package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// The benchmark fixture must compose tool nodes: a tool_result with no
// matching tool_use composes to nothing. The subject is the fixture.
func TestOpenRegionFixtureComposesToolNodes(t *testing.T) {
	const rounds = 4
	a := openRegionAgent(rounds, 200, false)
	pre, suf, _ := a.composeTurn(nil)
	nodes := append(append([]livedoc.Node(nil), pre...), suf...)

	var prose, tools, withOutput int
	for _, n := range nodes {
		switch n.Type {
		case livedoc.NodeProse:
			prose++
		case livedoc.NodeTool:
			tools++
			if n.Output != "" {
				withOutput++
			}
		}
	}
	if tools != rounds {
		t.Fatalf("fixture composed %d tool nodes, want %d: the benchmark is not measuring tools", tools, rounds)
	}
	if withOutput != rounds {
		t.Fatalf("fixture composed %d tool nodes carrying output, want %d: tool_result did not fold under its invoke", withOutput, rounds)
	}
	if prose != rounds {
		t.Fatalf("fixture composed %d prose nodes, want %d", prose, rounds)
	}
	for _, n := range nodes {
		if n.Type != livedoc.NodeTool {
			continue
		}
		if n.Status != livedoc.StatusOK {
			t.Fatalf("tool node %s status %q, want ok: a running tool skips the result path the benchmark exists to measure", n.ID, n.Status)
		}
		// Seeing the clamp proves the benchmark reaches the code that splits the full result text.
		if got := strings.Count(n.Output, "\n") + 1; got != 200 {
			t.Fatalf("tool node %s output is %d lines, want the 200-line clamp", n.ID, got)
		}
	}
}

// The assertion above is load-bearing: break the pairing and the tool output disappears.
func TestOpenRegionFixtureCanFail(t *testing.T) {
	a := openRegionAgent(4, 200, false)
	log := a.figLog.(*benchmarkLog)
	for i := range log.rows {
		for ci := range log.rows[i].Payload.Content {
			c := &log.rows[i].Payload.Content[ci]
			if c.ToolCallID != "" && c.Text != "" {
				c.ToolCallID += "_orphaned"
			}
		}
	}
	opre, osuf, _ := a.composeTurn(nil)
	orphaned := append(append([]livedoc.Node(nil), opre...), osuf...)
	for _, n := range orphaned {
		if n.Type == livedoc.NodeTool && n.Output != "" {
			t.Fatal("an orphaned tool_result still produced tool output: the fixture check cannot detect fault #7")
		}
	}
}

// One frame's node count is proportional to the whole open region.
func TestOpenRegionGrowsWithTheRegion(t *testing.T) {
	sp, ss, _ := openRegionAgent(4, 200, false).composeTurn(nil)
	smallNodes := append(append([]livedoc.Node(nil), sp...), ss...)
	lp, ls, _ := openRegionAgent(64, 200, false).composeTurn(nil)
	largeNodes := append(append([]livedoc.Node(nil), lp...), ls...)
	small, large := len(smallNodes), len(largeNodes)
	if large <= small {
		t.Fatalf("open region of 64 rounds composed %d nodes, 4 rounds composed %d", large, small)
	}
	if want := small * 16; large != want {
		t.Fatalf("16x the rounds composed %d nodes, want %d: the frame is not linear in the region", large, want)
	}
}
