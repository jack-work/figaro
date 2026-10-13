package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The frame benchmark's fixture, asserted rather than timed: a benchmark
// that stops composing beats any floor.
func TestEmitFrameBenchmarkActuallyComposes(t *testing.T) {
	const rounds = 64
	a := openRegionAgent(rounds, 200, true)
	a.ariaSrv = aria.NewServer()
	a.turnID = 1
	a.ariaSrv.OpenTurn(a.turnID)

	// Two frames, as the benchmark does: the second is the steady state it measures.
	a.emitDelta(a.composeTurn(nil))
	pre, suf, stable := a.composeTurn(nil)
	nodes := append(append([]livedoc.Node(nil), pre...), suf...)

	// The count comes from the oracle: the last round's tool result is absent (lastRunning).
	msgs := a.regionMessages()
	want := regionOracle(a)
	if len(msgs) != len(want) {
		t.Fatalf("the held region has %d messages, a full materialization has %d. "+
			"A benchmark over a truncated region measures a smaller problem than the one claimed",
			len(msgs), len(want))
	}
	if len(msgs) < rounds {
		t.Fatalf("the region holds %d messages for %d rounds; this fixture is too small to be the one claimed", len(msgs), rounds)
	}

	if len(nodes) != len(msgs)+1 {
		// One node per message, plus the running round's tool node.
		t.Fatalf("composed %d nodes over %d messages; the frame is not composing the whole region",
			len(nodes), len(msgs))
	}
	if stable == 0 {
		t.Fatalf("stable is 0: the composer memo is not being reused, so this fixture is not measuring the incremental path")
	}

	// The last tool node is the running one and is empty by design; assert over the settled ones.
	carriers, bytes := 0, 0
	for _, n := range nodes {
		if n.Type == livedoc.NodeTool && strings.Count(n.Output, strings.Repeat("x", 79)) >= 100 {
			carriers++
		}
		bytes += len(n.Output) + len(n.Markdown)
	}
	if carriers < rounds-1 {
		t.Fatalf("only %d of %d settled tool nodes carry their 200 output lines; the frame is composing empty nodes",
			carriers, rounds-1)
	}
	if bytes < 500<<10 {
		t.Fatalf("the whole frame carries %d bytes of node text; the fixture is not the one the numbers claim", bytes)
	}

	// A composed frame that emits nothing would make Update free.
	a.emitDelta(pre, suf, stable)
	if !a.ariaSrv.HasOpen() {
		t.Fatal("the server holds no open frame after emitDelta")
	}
	t.Logf("frame verified: %d messages held, %d nodes composed, stable=%d, %d tool nodes carrying late lines, %d bytes of node text",
		len(msgs), len(nodes), stable, carriers, bytes)
}

func BenchmarkEmitFrameStreamingPartial(b *testing.B) {
	a := openRegionAgent(64, 200, true)
	a.ariaSrv = aria.NewServer()
	a.turnID = 1
	a.ariaSrv.OpenTurn(a.turnID)
	a.emitDelta(a.composeTurn(nil))
	id := lastToolCallID(a)
	if id == "" {
		b.Fatal("the fixture has no running tool; this benchmark would measure the still frame")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.gov.Feed(id, "another streamed line of tool output\n")
		a.emitDelta(a.composeTurn(nil))
	}
}

func lastToolCallID(a *Agent) string {
	msgs := a.regionMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, c := range msgs[i].Content {
			if c.ToolCallID != "" {
				return c.ToolCallID
			}
		}
	}
	return ""
}
