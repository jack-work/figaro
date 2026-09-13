package aria

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// floorTurns is the fixture the floor tests page over: n turns of nodes whose
// sizes are fixed, so a budget is arithmetic rather than a guess. Turn i has
// i%3+2 nodes, which puts the floor tests' mid-turn anchors inside turns that
// actually have a middle.
func floorTurns(n int) []Turn {
	out := make([]Turn, 0, n)
	for i := 1; i <= n; i++ {
		t := Turn{ID: uint64(i), Sealed: true, Inquiry: fmt.Sprintf("q%d", i),
			LTs: []uint64{uint64(i)*10 + 1, uint64(i)*10 + 5}}
		for j := 0; j < i%3+2; j++ {
			t.Nodes = append(t.Nodes, livedoc.Node{
				Type:     livedoc.NodeProse,
				Role:     livedoc.RoleOutput,
				Markdown: strings.Repeat("x", 200+j),
				Src:      []livedoc.Src{{LT: uint64(i)*10 + 2}},
			})
		}
		out = append(out, t)
	}
	return out
}

// A floored backward read serves nothing below the floor. The floor is the
// bottom of the answer, not a hint.
func TestFloor_ServesNothingBelowIt(t *testing.T) {
	turns := floorTurns(10)
	p := PaginateBefore(turns, Anchor{}, Anchor{Turn: 7}, 1<<20)
	if len(p.Parts) == 0 {
		t.Fatal("a floored read of a reachable window returned nothing")
	}
	for _, part := range p.Parts {
		if part.ID < 7 {
			t.Fatalf("turn %d is below the floor and must not be on the page", part.ID)
		}
	}
	if got := p.Parts[0].ID; got != 7 {
		t.Fatalf("the page must begin at the floor turn, got %d", got)
	}
	if p.Parts[0].From != 0 || p.Parts[0].ClippedHead {
		t.Fatalf("a floor at node 0 keeps the whole turn, got from=%d clipped=%v",
			p.Parts[0].From, p.Parts[0].ClippedHead)
	}
}

// More.Before is the floor's answer to "is there history I did not send you":
// set when the floor cut the walk, clear when the floor stands at or below the
// oldest node there is.
func TestFloor_MoreBeforeSaysWhetherItCut(t *testing.T) {
	turns := floorTurns(10)

	cut := PaginateBefore(turns, Anchor{}, Anchor{Turn: 7}, 1<<20)
	if !cut.More.Before {
		t.Fatal("the floor cut six turns off the page; More.Before must say so")
	}
	if cut.Prev == nil {
		t.Fatal("More.Before without a Prev cursor leaves a client unable to ask")
	}

	for _, floor := range []Anchor{{Turn: 1}, {Turn: 1, Node: 0}} {
		p := PaginateBefore(turns, Anchor{}, floor, 1<<20)
		if p.More.Before {
			t.Fatalf("floor %+v is at the oldest node: nothing precedes it", floor)
		}
		if p.Prev != nil {
			t.Fatalf("floor %+v: a Prev cursor points at history that is not there", floor)
		}
	}
}

// The floor is INCLUSIVE of its own anchor: a floor of (t, 3) keeps nodes 3
// and up of turn t and nothing of turn t-1.
func TestFloor_IsInclusiveOfItsOwnAnchor(t *testing.T) {
	turns := floorTurns(10)
	const floorTurn, floorNode = 8, 3
	if n := len(turns[floorTurn-1].Nodes); n <= floorNode {
		t.Fatalf("fixture: turn %d has %d nodes, the test needs more than %d", floorTurn, n, floorNode)
	}

	p := PaginateBefore(turns, Anchor{}, Anchor{Turn: floorTurn, Node: floorNode}, 1<<20)
	first := p.Parts[0]
	if first.ID != floorTurn {
		t.Fatalf("the page must begin in the floor's own turn, got %d", first.ID)
	}
	if first.From != floorNode {
		t.Fatalf("the floor's own node is kept: want from=%d, got %d", floorNode, first.From)
	}
	if !first.ClippedHead {
		t.Fatal("a page beginning mid-turn must say its head is clipped")
	}
	want := len(turns[floorTurn-1].Nodes) - floorNode
	if got := len(first.Nodes); got != want {
		t.Fatalf("want nodes %d.. (%d of them), got %d", floorNode, want, got)
	}
	if first.Nodes[0].Markdown != turns[floorTurn-1].Nodes[floorNode].Markdown {
		t.Fatal("the first node on the page is not the floor's own node")
	}
	for _, part := range p.Parts {
		if part.ID < floorTurn {
			t.Fatalf("turn %d is below the floor's turn and must be absent", part.ID)
		}
	}
}

// A floor naming a node past its turn's end, or a turn the aria never had,
// resolves UP: the next position that exists, never down into history the
// caller said it holds.
func TestFloor_ResolvesUpwardWhenItNamesNothing(t *testing.T) {
	turns := floorTurns(10)

	past := PaginateBefore(turns, Anchor{}, Anchor{Turn: 6, Node: 99}, 1<<20)
	if past.Parts[0].ID != 7 || past.Parts[0].From != 0 {
		t.Fatalf("a floor past turn 6's last node starts at turn 7 node 0, got turn %d node %d",
			past.Parts[0].ID, past.Parts[0].From)
	}

	gapped := floorTurns(10)
	gapped = append(gapped[:5], gapped[6:]...) // turn 6 never happened
	p := PaginateBefore(gapped, Anchor{}, Anchor{Turn: 6}, 1<<20)
	if p.Parts[0].ID != 7 {
		t.Fatalf("a floor on a missing turn keeps what is above it, got turn %d", p.Parts[0].ID)
	}
}

// A backward read whose whole reach is below the floor has nothing to send:
// the caller already holds every part at or above it.
func TestFloor_AboveTheAnchorIsAnEmptyPage(t *testing.T) {
	turns := floorTurns(10)
	p := PaginateBefore(turns, Anchor{Turn: 4, Node: 0}, Anchor{Turn: 7}, 1<<20)
	if len(p.Parts) != 0 {
		t.Fatalf("want an empty page, got %d parts starting at turn %d", len(p.Parts), p.Parts[0].ID)
	}
}

// THE COMPATIBILITY CANARY. Every caller today passes a zero floor, so a zero
// floor must page exactly as the unfloored walk did, byte for byte. The golden
// was generated by the code this branch was cut from (02b6a1e2), over the same
// 40-turn fixture; regenerate it only against that code, never against this.
func TestFloor_ZeroFloorIsTheAnswerFromBefore(t *testing.T) {
	got, err := json.MarshalIndent(zeroFloorWalk(floorTurns(40)), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "floor_zero_pages.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("a zero floor changed the page the unfloored walk produced.\nwant %s\n\ngot %s", want, got)
	}
}

// zeroFloorWalk is the whole backward walk of the fixture at three budgets,
// page by page from the tail: what a client scrolling up receives. Kept here
// so the golden's generator and its checker are the same function.
func zeroFloorWalk(turns []Turn) []Page {
	var out []Page
	for _, budget := range []int{4 << 10, 1 << 16} {
		at := Anchor{}
		for {
			p := PaginateBefore(turns, at, Anchor{}, budget)
			if len(p.Parts) == 0 {
				break
			}
			out = append(out, p)
			if p.Prev == nil {
				break
			}
			at = *p.Prev
		}
	}
	return out
}
