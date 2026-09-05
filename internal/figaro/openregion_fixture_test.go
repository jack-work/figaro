package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// The benchmarks in openregion_bench_test.go are only worth their numbers if
// the fixture composes what a real open turn composes. Fixture fault #7 of the
// previous campaign was a compose test whose tool_result had no matching
// tool_use: a result alone composes to NOTHING, and the benchmark would have
// measured prose while reporting on tools.
//
// TestOpenRegionFixtureComposesToolNodes asserts the fact. The subject is the
// FIXTURE, not composeTurn.
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
		// tailBound clamps to the last composeBashCap source lines. Seeing the
		// clamp proves the benchmark reaches the code that splits the FULL
		// result text, which is the allocation under measurement.
		if got := strings.Count(n.Output, "\n") + 1; got != 200 {
			t.Fatalf("tool node %s output is %d lines, want the 200-line clamp", n.ID, got)
		}
	}
}

// TestOpenRegionFixtureCanFail proves the assertion above can fail:
// break the invoke/result pairing the way fault #7 did, and the tool output
// the benchmark measures disappears. If this test ever fails, the check above
// has stopped being able to catch the fault it was written for.
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

// TestOpenRegionGrowsWithTheRegion states the S6 claim as a fact rather than
// as a benchmark line: one frame's node count is proportional to the whole
// open region, not to what changed since the previous frame.
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
