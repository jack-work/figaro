package aria

import (
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// cursorTurns is an aria of whole turns, each with a question and three nodes.
func cursorTurns(n int) []Turn {
	out := make([]Turn, 0, n)
	for i := range n {
		id := uint64(i + 1)
		out = append(out, Turn{
			ID: id, Sealed: true, Inquiry: "q",
			Nodes: []livedoc.Node{prose("a"), prose("b"), prose("c")},
		})
	}
	return out
}

// TestCursorsWalkTheWholeAria: a client that follows Next from the head, and
// Prev from the tail, sees every node exactly once and never has to know how
// long the aria is. That is the whole of paging.
func TestCursorsWalkTheWholeAria(t *testing.T) {
	turns := cursorTurns(40)
	const budget = 400 // small enough to cut turns in half

	seen := map[Anchor]int{}
	at, pages := Anchor{}, 0
	for {
		p := Paginate(turns, at, Forward, budget)
		if len(p.Parts) == 0 {
			t.Fatalf("page %d at %v came back empty", pages, at)
		}
		pages++
		for _, part := range p.Parts {
			for i := range part.Nodes {
				seen[Anchor{Turn: part.ID, Node: part.From + uint64(i)}]++
			}
		}
		if !p.More.After {
			if p.Next != nil {
				t.Fatalf("page %d says nothing follows but offers a cursor %v", pages, *p.Next)
			}
			break
		}
		if p.Next == nil {
			t.Fatalf("page %d says more follows and offers no cursor", pages)
		}
		at = *p.Next
		if pages > len(turns)*4 {
			t.Fatal("the walk did not terminate")
		}
	}
	if pages < 3 {
		t.Fatalf("fixture: %d pages, want a walk of several", pages)
	}
	for _, tn := range turns {
		for i := range tn.Nodes {
			a := Anchor{Turn: tn.ID, Node: uint64(i)}
			if seen[a] != 1 {
				t.Fatalf("node %v was read %d times, want exactly once", a, seen[a])
			}
		}
	}

	// And backward, from the tail, with Prev.
	back := map[Anchor]int{}
	at, pages = Anchor{}, 0
	for {
		p := PaginateBefore(turns, at, budget)
		if len(p.Parts) == 0 {
			break
		}
		pages++
		for _, part := range p.Parts {
			for i := range part.Nodes {
				back[Anchor{Turn: part.ID, Node: part.From + uint64(i)}]++
			}
		}
		if !p.More.Before {
			break
		}
		if p.Prev == nil {
			t.Fatalf("backward page %d says more precedes it and offers no cursor", pages)
		}
		at = *p.Prev
		if pages > len(turns)*4 {
			t.Fatal("the backward walk did not terminate")
		}
	}
	for a, n := range seen {
		if back[a] != n {
			t.Fatalf("node %v: forward saw it %d times, backward %d", a, n, back[a])
		}
	}
}

// TestReadAtACoordinateStartsThere: the cursor is an address, so a reader that
// has never seen the aria can ask for any part of it and get that part.
func TestReadAtACoordinateStartsThere(t *testing.T) {
	turns := cursorTurns(200)
	for _, want := range []Anchor{{Turn: 1}, {Turn: 7, Node: 2}, {Turn: 150}, {Turn: 200, Node: 2}} {
		p := Paginate(turns, want, Forward, 400)
		if len(p.Parts) == 0 {
			t.Fatalf("no page at %v", want)
		}
		from, _ := p.Span()
		if from != want {
			t.Fatalf("a read at %v began at %v", want, from)
		}
	}
}
