package aria

import (
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// floorServer is a bounded server over history it must recompose to read: the
// shape the widen-and-recut loop exists for.
func floorServer(t testing.TB, turns []Turn, budget int64) (*Server, *int) {
	t.Helper()
	history := map[uint64]Turn{}
	recomposed := new(int)
	cc := NewComposedCache(fwtree.NewBudget(budget), composerFor(history, recomposed), nil)
	s := boundTo(cc, "aria")
	for _, tn := range turns {
		history[tn.ID] = tn
		s.Commit(tn)
	}
	cc.Budget().Settle(2e9)
	*recomposed = 0
	return s, recomposed
}

// The window is an optimization, not a different answer: a floored read off a
// bounded server is the floored read off history entire. This is the widen-and
// -recut loop's canary. A page the FLOOR cut is not a page the WINDOW cut, and
// a loop that confuses the two either spins or reports More wrongly.
func TestFloor_WindowedReadMatchesTheFullWalk(t *testing.T) {
	turns := floorTurns(40)
	bounded, recomposed := floorServer(t, turns, 32<<10)
	full := NewServer()
	for _, tn := range turns {
		full.Commit(tn)
	}

	for _, floor := range []Anchor{{Turn: 30}, {Turn: 30, Node: 1}, {Turn: 1}, {Turn: 40, Node: 2}} {
		for _, at := range []Anchor{{}, {Turn: 39, Node: 0}} {
			for _, budget := range []int{600, 4 << 10, 1 << 16} {
				a := bounded.ReadBefore(at, floor, budget)
				b := full.ReadBefore(at, floor, budget)
				if len(a.Parts) != len(b.Parts) {
					t.Fatalf("floor=%+v at=%+v budget=%d: %d parts vs %d",
						floor, at, budget, len(a.Parts), len(b.Parts))
				}
				for i := range a.Parts {
					if a.Parts[i].ID != b.Parts[i].ID || a.Parts[i].From != b.Parts[i].From ||
						len(a.Parts[i].Nodes) != len(b.Parts[i].Nodes) {
						t.Fatalf("floor=%+v at=%+v budget=%d part %d: %+v vs %+v",
							floor, at, budget, i, a.Parts[i], b.Parts[i])
					}
					if a.Parts[i].ID < floor.Turn {
						t.Fatalf("floor=%+v: turn %d is below it", floor, a.Parts[i].ID)
					}
				}
				if a.More.Before != b.More.Before || a.More.After != b.More.After {
					t.Fatalf("floor=%+v at=%+v budget=%d: More %+v vs %+v",
						floor, at, budget, a.More, b.More)
				}
			}
		}
	}
	if *recomposed == 0 {
		t.Fatal("the bounded server never recomposed: the test exercised nothing")
	}
}

// The floor bounds the WINDOW as well as the page: turns below it are never
// composed. Without that, a floored read saves wire bytes and pays the same
// composition cost, which is most of what a hop costs on a cold aria.
func TestFloor_ComposesNothingBelowIt(t *testing.T) {
	turns := floorTurns(40)

	floored, flooredRecomposes := floorServer(t, turns, 32<<10)
	page := floored.ReadBefore(Anchor{}, Anchor{Turn: 38}, 1<<16)
	if len(page.Parts) == 0 {
		t.Fatal("the floored read returned nothing")
	}

	open, openRecomposes := floorServer(t, turns, 32<<10)
	openPage := open.ReadBefore(Anchor{}, Anchor{}, 1<<16)

	if *flooredRecomposes >= *openRecomposes {
		t.Fatalf("a floor at turn 38 must compose less than an unfloored read: %d vs %d recompositions",
			*flooredRecomposes, *openRecomposes)
	}
	if len(page.Parts) >= len(openPage.Parts) {
		t.Fatalf("the floored page must be the shorter one: %d parts vs %d",
			len(page.Parts), len(openPage.Parts))
	}
	if !page.More.Before {
		t.Fatal("the floor cut 37 turns off: More.Before must say so")
	}
}
