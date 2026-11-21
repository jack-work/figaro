package aria

import (
	"testing"
)

func partIDs(p Page) []uint64 {
	var out []uint64
	for _, part := range p.Parts {
		out = append(out, part.ID)
	}
	return out
}

func sameIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUntil_StopsAForwardReadBeforeItsAnchor(t *testing.T) {
	turns := floorTurns(10)
	p := PaginateUntil(turns, Anchor{Turn: 3}, Anchor{Turn: 6}, 1<<20)
	if got := partIDs(p); !sameIDs(got, []uint64{3, 4, 5}) {
		t.Fatalf("[3,6) read turns %v", got)
	}
	if !p.More.After || p.Next == nil || *p.Next != (Anchor{Turn: 6}) {
		t.Fatalf("a ceiling cut says there is more and where: More=%+v Next=%v", p.More, p.Next)
	}
	last := p.Parts[len(p.Parts)-1]
	if last.ClippedTail {
		t.Fatal("a ceiling at node 0 keeps the turn before it whole")
	}
}

func TestUntil_CutsMidTurnAtItsNode(t *testing.T) {
	turns := floorTurns(10)
	p := PaginateUntil(turns, Anchor{Turn: 5}, Anchor{Turn: 6, Node: 1}, 1<<20)
	if got := partIDs(p); !sameIDs(got, []uint64{5, 6}) {
		t.Fatalf("read turns %v", got)
	}
	tail := p.Parts[1]
	if len(tail.Nodes) != 1 || !tail.ClippedTail {
		t.Fatalf("turn 6 below node 1 is its node 0 alone: %d nodes, clipped=%v", len(tail.Nodes), tail.ClippedTail)
	}
}

func TestUntil_AtOrBeforeTheStartReadsNothing(t *testing.T) {
	turns := floorTurns(10)
	for _, until := range []Anchor{{Turn: 4}, {Turn: 2}} {
		if p := PaginateUntil(turns, Anchor{Turn: 4}, until, 1<<20); len(p.Parts) != 0 {
			t.Fatalf("until %+v from turn 4 read %v", until, partIDs(p))
		}
	}
}

func TestUntil_PastTheEndIsNoCeiling(t *testing.T) {
	turns := floorTurns(10)
	a := PaginateUntil(turns, Anchor{Turn: 2}, Anchor{Turn: 99}, 1<<20)
	b := Paginate(turns, Anchor{Turn: 2}, Forward, 1<<20)
	if !sameIDs(partIDs(a), partIDs(b)) || a.More != b.More {
		t.Fatalf("until past the end changed the read: %v %+v vs %v %+v", partIDs(a), a.More, partIDs(b), b.More)
	}
}

func TestUntil_ForwardAndBackwardReadTheSameHalfOpenRange(t *testing.T) {
	turns := floorTurns(12)
	for _, r := range [][2]uint64{{1, 13}, {2, 7}, {5, 6}, {9, 12}} {
		lo, hi := Anchor{Turn: r[0]}, Anchor{Turn: r[1]}
		fwd := PaginateUntil(turns, lo, hi, 1<<20)
		back := PaginateBefore(turns, hi, lo, 1<<20)
		if !sameIDs(partIDs(fwd), partIDs(back)) {
			t.Fatalf("[%d,%d): forward %v, backward %v", r[0], r[1], partIDs(fwd), partIDs(back))
		}
		for i := range fwd.Parts {
			if len(fwd.Parts[i].Nodes) != len(back.Parts[i].Nodes) {
				t.Fatalf("[%d,%d) turn %d: %d nodes forward, %d backward", r[0], r[1], fwd.Parts[i].ID, len(fwd.Parts[i].Nodes), len(back.Parts[i].Nodes))
			}
		}
	}
}

func TestUntil_WindowedReadMatchesTheFullWalk(t *testing.T) {
	turns := floorTurns(40)
	bounded, recomposed := floorServer(t, turns, 32<<10)
	full := NewServer()
	for _, tn := range turns {
		full.Commit(tn)
	}
	for _, until := range []Anchor{{Turn: 12}, {Turn: 12, Node: 1}, {Turn: 40, Node: 2}, {Turn: 41}, {Turn: 3}} {
		for _, at := range []Anchor{{}, {Turn: 5}, {Turn: 11, Node: 1}} {
			for _, budget := range []int{600, 4 << 10, 1 << 16} {
				a := bounded.ReadUntil(at, until, budget)
				b := full.ReadUntil(at, until, budget)
				if !sameIDs(partIDs(a), partIDs(b)) {
					t.Fatalf("until=%+v at=%+v budget=%d: %v vs %v", until, at, budget, partIDs(a), partIDs(b))
				}
				for i := range a.Parts {
					if a.Parts[i].From != b.Parts[i].From || len(a.Parts[i].Nodes) != len(b.Parts[i].Nodes) {
						t.Fatalf("until=%+v at=%+v budget=%d part %d differs", until, at, budget, i)
					}
					if a.Parts[i].ID > until.Turn || (a.Parts[i].ID == until.Turn && until.Node == 0) {
						t.Fatalf("until=%+v: turn %d is at or past it", until, a.Parts[i].ID)
					}
				}
				if a.More != b.More {
					t.Fatalf("until=%+v at=%+v budget=%d: More %+v vs %+v", until, at, budget, a.More, b.More)
				}
			}
		}
	}
	if *recomposed == 0 {
		t.Fatal("the bounded server never recomposed: the test exercised nothing")
	}
}

func TestUntil_ComposesNothingPastIt(t *testing.T) {
	turns := floorTurns(40)
	ceiled, ceiledRecomposes := floorServer(t, turns, 32<<10)
	if page := ceiled.ReadUntil(Anchor{Turn: 1}, Anchor{Turn: 3}, 1<<16); len(page.Parts) == 0 {
		t.Fatal("the ceiled read returned nothing")
	}
	open, openRecomposes := floorServer(t, turns, 32<<10)
	open.ReadUntil(Anchor{Turn: 1}, Anchor{}, 1<<16)
	if *ceiledRecomposes >= *openRecomposes {
		t.Fatalf("a ceiling at turn 3 must compose less than an open read: %d vs %d recompositions", *ceiledRecomposes, *openRecomposes)
	}
}
