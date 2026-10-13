package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	livelog "github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/sdk/aria"
)

const productionPageBudget = 65536 // config.defaultPageBudget

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
	// 320 KB: a backward page cannot span it.
	big := make([]livedoc.Node, 40)
	for i := range big {
		big[i] = node(1000+i, 8000)
	}
	turns = append(turns, aria.Turn{ID: 4, Sealed: true, Inquiry: "QUESTION4", Nodes: big})
	return turns
}

// The wire's own pagination, at the production budget, emits a mid-turn part.
func TestSpeakerHeader_RealPageWalkProducesTheSeam(t *testing.T) {
	turns := realisticTurns()
	last := turns[len(turns)-1]
	page := livelog.PaginateBefore(turns,
		aria.Anchor{Turn: last.ID, Node: uint64(len(last.Nodes) - 1)}, aria.Anchor{}, productionPageBudget)

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

	msgs := pageMessages(page)
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
