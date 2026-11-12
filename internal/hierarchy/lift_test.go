package hierarchy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
)

func TestAKillThatLiftsDrawsTheChildrenUnderTheParentAndTakesTheNodeAlone(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	p, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Lift: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != "X" {
		t.Fatalf("removed %v, want X alone", f.removed)
	}
	for _, kid := range []string{"C", "D"} {
		if f.over[kid] != "P" || f.pins[kid] != "P" {
			t.Fatalf("%s drawn under %q, pinned under %q; want P for both", kid, f.over[kid], f.pins[kid])
		}
	}
	lifted := 0
	for _, m := range p.Moves {
		if m.Lift {
			lifted++
		}
	}
	if lifted != 2 {
		t.Fatalf("the plan lifts %d, want C and D: %+v", lifted, p.Moves)
	}
}

func TestAKillThatLiftsAtTheTopDrawsTheChildrenAtTheTop(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "P", Lift: true}); err != nil {
		t.Fatal(err)
	}
	if f.over["X"] != "stump" {
		t.Fatalf("X drawn under %q, want the stump", f.over["X"])
	}
}

func TestALiftIsNeverRecursive(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	_, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Lift: true, Recursive: true})
	if !errors.Is(err, hierarchy.ErrLiftRecursive) {
		t.Fatalf("a recursive lift = %v, want ErrLiftRecursive", err)
	}
	if f.writes != 0 || f.removed != nil {
		t.Fatal("a refused lift wrote")
	}
}

func TestARefusedLiftWritesNothing(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, guardFunc(func(_ hierarchy.Principal, p hierarchy.Plan) error {
		if p.Lift {
			return errors.New("no")
		}
		return nil
	}))
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Lift: true}); err == nil {
		t.Fatal("the guard's refusal was ignored")
	}
	if f.writes != 0 || f.removed != nil {
		t.Fatal("a refused lift wrote")
	}
}

func TestAFailedRemoveOrReapPutsTheLiftedChildrenBack(t *testing.T) {
	f := chain()
	f.over["D"] = "C"
	f.removeErr = errors.New("disk full")
	h := hierarchy.New(f, hierarchytest.Open{})
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Lift: true}); !errors.Is(err, f.removeErr) {
		t.Fatalf("kill = %v, want the store's error", err)
	}
	if _, drawn := f.over["C"]; drawn || f.over["D"] != "C" {
		t.Fatalf("after a failed kill the overrides are %v, want D under C alone", f.over)
	}

	_, err := h.Under(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Reap, Node: "C", Lift: true}, func(p hierarchy.Plan) error {
		if up, _ := f.Drawn("D"); up != "X" {
			t.Fatalf("the reap's commit ran with D under %q, before it was lifted to X", up)
		}
		return errors.New("reap failed")
	})
	if err == nil {
		t.Fatal("the commit's error was lost")
	}
	if f.over["D"] != "C" {
		t.Fatalf("after a failed reap D is drawn under %q, want C", f.over["D"])
	}
}
