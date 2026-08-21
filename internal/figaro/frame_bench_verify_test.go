package figaro

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// IS THE FRAME BENCHMARK STILL DOING THE WORK?
//
// The read-side change beat its own pre-registered floor by 20x. A number that
// beats a floor derived from what the region physically contains is first a
// suspect and only then a result: the likeliest cause is that the benchmark
// stopped composing. This project has measured 209 ns of nothing before.
//
// So: the same fixture the benchmark uses, asserted rather than timed.
func TestEmitFrameBenchmarkActuallyComposes(t *testing.T) {
	const rounds = 64
	a := openRegionAgent(rounds, 200, true)
	a.ariaSrv = aria.NewServer()
	a.turnID = 1
	a.ariaSrv.OpenTurn(a.turnID)

	// TWO frames, as the benchmark does: the first publishes everything (there
	// is no previous frame to be stable against), and it is the SECOND that
	// the benchmark's steady state measures. Asserting on the first reports
	// stable=0 and says the memo is dead, which it is not -- that mistake was
	// made here first.
	a.emitDelta(a.composeTurn(nil))
	pre, suf, stable := a.composeTurn(nil)
	nodes := append(append([]livedoc.Node(nil), pre...), suf...)

	// (1) THE REGION IS FULLY MATERIALIZED, not skipped. The count is taken
	// from the ORACLE, not from arithmetic over the fixture: the last round's
	// tool result is deliberately absent (lastRunning), and an expectation of
	// rounds*2 fails by one for that reason alone -- which it did, on the
	// first run of this test.
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

	// (2) THE FRAME COMPOSES, and it composes the WHOLE region.
	if len(nodes) != len(msgs)+1 {
		// One node per message, plus the tool node of the round whose result
		// has not landed: its invoke composes even though no result exists.
		t.Fatalf("composed %d nodes over %d messages; the frame is not composing the whole region",
			len(nodes), len(msgs))
	}
	if stable == 0 {
		t.Fatalf("stable is 0: the composer memo is not being reused, so this fixture is not measuring the incremental path")
	}

	// (3) LATE TOOL LINES ARE PRESENT. Node count alone can be satisfied by
	// empty nodes; the tool output is what the region actually carries. The
	// LAST tool node is the running one and is empty by design (no partial has
	// been fed), so the assertion is over the settled ones -- checking the
	// last node reported "len 0" and looked like a dead benchmark when it was
	// a correct one.
	carriers, bytes := 0, 0
	for _, n := range nodes {
		// The fixture's tool output is 200 lines of 79 x's; a settled tool node
		// must carry them. "tool output line N" is the COMPOSE package's
		// fixture, not this one -- asserting on the wrong fixture's text
		// reported "0 of 63 carry their output" against a frame that was
		// carrying all of it.
		if n.Type == livedoc.NodeTool && strings.Count(n.Output, strings.Repeat("x", 79)) >= 100 {
			carriers++
		}
		bytes += len(n.Output) + len(n.Markdown)
	}
	if carriers < rounds-1 {
		t.Fatalf("only %d of %d settled tool nodes carry their 200 output lines; the frame is composing empty nodes",
			carriers, rounds-1)
	}
	// 64 rounds x 200 lines x 80 bytes is ~1 MB of tool output; the floor is
	// half that, so it fails on a fixture that shrank rather than on an
	// arithmetic quibble. (It first read 1<<20 and failed at 1,009,217.)
	if bytes < 500<<10 {
		t.Fatalf("the whole frame carries %d bytes of node text; the fixture is not the one the numbers claim", bytes)
	}

	// (4) THE FRAME REACHES THE SERVER. A composed frame that emits nothing
	// would make Update free and the measurement hollow.
	a.emitDelta(pre, suf, stable)
	if !a.ariaSrv.HasOpen() {
		t.Fatal("the server holds no open frame after emitDelta")
	}
	t.Logf("frame verified: %d messages held, %d nodes composed, stable=%d, %d tool nodes carrying late lines, %d bytes of node text",
		len(msgs), len(nodes), stable, carriers, bytes)
}

// PROFILE OF THE AFTER-STATE, 60,000 streaming frames, recorded here because
// it is the answer to "did the benchmark stop doing the work":
//
//	32.9%  bytealg.LastIndexByteString   (tailBound, clamping the growing tail)
//	17.7%  compose.memoKey.matches
//	 8.2%  compose.(*Incremental).valid
//	 4.7%  compose.stableBoundary
//	 -     NOTHING for reading or materializing the region
//
// The region re-read does not appear at all, which is the claim. What is left
// is the memo's validity guard and the clamp on the one node that is actually
// moving.
//
// AND THE HONEST CAVEAT, MEASURED: BenchmarkEmitFrame re-emits a frame in
// which NOTHING changed. Production's streaming frame changes one partial per
// event, which is the case the campaign's headline numbers are about. This
// benchmark drives that case instead, so the two can be compared rather than
// confused.
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
