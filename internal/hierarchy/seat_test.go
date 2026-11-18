package hierarchy_test

import (
	"context"
	"maps"
	"testing"

	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
)

func seatMoves(t *testing.T, f *fakeEngine, pred, succ string) (map[string]string, error) {
	t.Helper()
	var seen hierarchy.Plan
	h := hierarchy.New(f, guardFunc(func(_ hierarchy.Principal, p hierarchy.Plan) error { seen = p; return nil }))
	_, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Seat, Node: pred, To: succ})
	got := map[string]string{}
	for _, m := range seen.Moves {
		got[m.Node] = m.From + ">" + m.To
	}
	return got, err
}

func TestASeatOfAForkSwapsItWithItsPredecessorInOnePlan(t *testing.T) {
	f := chain()
	got, err := seatMoves(t, f, "X", "C")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"C": "X>P", "X": "P>C", "D": "X>C"}
	if !maps.Equal(got, want) {
		t.Fatalf("seat C in X's place moved %v, want %v", got, want)
	}
	if f.writes != 1 {
		t.Fatalf("the seat wrote %d times, want one write for the whole plan", f.writes)
	}
}

func TestASeatOfAFigaroFromElsewhereTakesThePlaceAndTheChildren(t *testing.T) {
	f := chain()
	f.stored["S"] = "stump"
	got, err := seatMoves(t, f, "X", "S")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"S": "stump>P", "X": "P>S", "C": "X>S", "D": "X>S"}
	if !maps.Equal(got, want) {
		t.Fatalf("seat S in X's place moved %v, want %v", got, want)
	}
}

func TestASeatOfADeeperDescendantLiftsItOverItsOwnAncestors(t *testing.T) {
	f := chain()
	f.stored["G"] = "C"
	got, err := seatMoves(t, f, "X", "G")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"G": "C>P", "X": "P>G", "C": "X>G", "D": "X>G"}
	if !maps.Equal(got, want) {
		t.Fatalf("seat G in X's place moved %v, want %v", got, want)
	}
}

func TestASeatThatHasLandedMovesNothing(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Seat, Node: "X", To: "C"}); err != nil {
		t.Fatal(err)
	}
	writes := f.writes
	p, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Seat, Node: "X", To: "C"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Moves) != 0 || f.writes != writes {
		t.Fatalf("a landed seat planned %v and wrote %d more times, want nothing", p.Moves, f.writes-writes)
	}
}

func TestASeatByAnAncestorIsRefused(t *testing.T) {
	f := chain()
	f.stored["Y"] = "C"
	h := hierarchy.New(f, hierarchytest.Open{})
	for _, succ := range []string{"P", "X"} {
		if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Seat, Node: "Y", To: succ}); err == nil {
			t.Fatalf("seating %s in Y's place, which it is drawn above, was allowed", succ)
		}
	}
	if f.writes != 0 {
		t.Fatal("a refused seat wrote an edge")
	}
}

func TestASeatNeverWaitsOnConsent(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, newParkGuard("P"))
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Seat, Node: "X", To: "C"}); err == nil {
		t.Fatal("a seat the guard parks was applied")
	}
	if n := len(h.Parked("P")); n != 0 {
		t.Fatalf("a seat left %d proposals parked, want none", n)
	}
	if f.writes != 0 {
		t.Fatal("a seat that would park wrote an edge")
	}
}
