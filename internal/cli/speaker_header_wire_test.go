package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// The whole chain, in production functions, with the production page budget.
//
// The other speaker-header probes start from a Page (or a Message) written by
// hand, which proves what the renderer DOES with a mid-turn slice and not that
// anything ever hands it one. That was the open link: if the wire never emitted
// a part with From>0, the ghost would be unreachable and the fix inert, and
// every one of those probes would still be green.
//
// This closes it from the other end:
//
//	aria.PaginateBefore: the real byte-budget walk, at the real 64 KiB
//	  -> assemble         sets ClippedHead when the window opens mid-turn
//	  -> committedMessages drops the inquiry for that part
//	  -> Composer.Message  must therefore draw no speaker header
//
// The turn sizes are not invented either. Measured over four of this store's
// own arias, turns over the 64 KiB budget are ordinary, 4 of 24, 3 of 18, 9 of
// 15, with the largest at 343 KB, five times the budget. A page walking back
// through any of them opens mid-turn by arithmetic, not by luck.
const productionPageBudget = 65536 // config.defaultPageBudget

// realisticTurns builds turns whose node sizes are in the range this store
// actually holds: a few small ones, then one that is comfortably over budget.
func realisticTurns() []aria.Turn {
	node := func(id int, size int) livedoc.Node {
		return livedoc.Node{
			Type: livedoc.NodeTool, Role: livedoc.RoleOutput,
			ID: fmt.Sprintf("n%d", id), Name: "bash", Status: livedoc.StatusOK,
			Summary: "rg --line-number transcript internal/cli",
			Output:  strings.Repeat("x", size),
		}
	}
	var turns []aria.Turn
	for i := 1; i <= 3; i++ {
		turns = append(turns, aria.Turn{
			ID: uint64(i), Sealed: true, Inquiry: fmt.Sprintf("QUESTION%d", i),
			Nodes: []livedoc.Node{node(i*10, 4000), node(i*10+1, 4000)},
		})
	}
	// The oversize turn: 40 nodes of 8 KB is 320 KB, in the measured range and
	// five times the budget, so a backward page cannot span it.
	big := make([]livedoc.Node, 40)
	for i := range big {
		big[i] = node(1000+i, 8000)
	}
	turns = append(turns, aria.Turn{ID: 4, Sealed: true, Inquiry: "QUESTION4", Nodes: big})
	return turns
}

// TestSpeakerHeader_RealPageWalkProducesTheSeam is the missing link: the wire's
// own pagination, at the production budget, emits a mid-turn part.
func TestSpeakerHeader_RealPageWalkProducesTheSeam(t *testing.T) {
	turns := realisticTurns()
	last := turns[len(turns)-1]
	// Page backward from the tail, exactly as the pager's prefetch does.
	page := aria.PaginateBefore(turns,
		aria.Anchor{Turn: last.ID, Node: uint64(len(last.Nodes) - 1)}, productionPageBudget)

	var clipped *aria.TurnPart
	for i := range page.Parts {
		if page.Parts[i].ClippedHead {
			clipped = &page.Parts[i]
			break
		}
	}
	if clipped == nil {
		t.Fatalf("the real page walk produced no mid-turn part at budget %d; "+
			"if this is genuinely unreachable then the ghost is too, and the "+
			"speaker-header fix guards nothing (parts: %d)", productionPageBudget, len(page.Parts))
	}
	if clipped.From == 0 {
		t.Fatalf("ClippedHead part with From=0: %+v", clipped.From)
	}
	t.Logf("wire emitted a mid-turn part: turn %d, From=%d, ClippedHead=%v",
		clipped.Turn.ID, clipped.From, clipped.ClippedHead)

	// ...and the renderer must not announce the speaker over it.
	msgs := committedMessages(page)
	var seen int
	for _, m := range msgs {
		if m.From == 0 {
			continue
		}
		seen++
		if hasHeader(headerComposer().Message(m, 80)) {
			t.Fatalf("a continuation slice the WIRE produced (turn %d, From=%d) "+
				"drew a speaker header with no question above it", m.Turn, m.From)
		}
	}
	if seen == 0 {
		t.Fatal("no continuation message reached the renderer; the chain is broken above the composer")
	}
	t.Logf("%d continuation slice(s) rendered, none announced the speaker", seen)
}
