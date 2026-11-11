package hierarchy_test

import (
	"context"
	"errors"
	"maps"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
)

type fakeEngine struct {
	stored   map[string]string
	over     map[string]string
	owner    map[string]string
	zone     map[string]string
	deadline map[string]int64
	refuse   bool
	writes   int
	removed  []string
	pins     map[string]string
}

func chain() *fakeEngine {
	return &fakeEngine{
		stored:   map[string]string{"stump": "", "P": "stump", "X": "P", "C": "X", "D": "X"},
		over:     map[string]string{},
		owner:    map[string]string{},
		zone:     map[string]string{},
		deadline: map[string]int64{},
	}
}

func (f *fakeEngine) Version() hierarchy.Version {
	return hierarchy.Version{Epoch: "e", Drawn: uint64(f.writes)}
}

func (f *fakeEngine) Node(id string) (hierarchy.Node, bool) {
	if _, ok := f.stored[id]; !ok {
		return hierarchy.Node{}, false
	}
	return hierarchy.Node{ID: id, Aria: id != "stump", Stump: "stump", Owner: f.owner[id]}, true
}

func (f *fakeEngine) Drawn(id string) (string, bool) {
	if p, ok := f.over[id]; ok {
		return p, true
	}
	return f.Stored(id)
}

func (f *fakeEngine) Stored(id string) (string, bool) { p, ok := f.stored[id]; return p, ok }

func (f *fakeEngine) DrawnChildren(id string) []string {
	var out []string
	for n := range f.stored {
		if p, _ := f.Drawn(n); p == id && n != id {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeEngine) Overrides() map[string]string { return maps.Clone(f.over) }

func (f *fakeEngine) DeleteSet(id string) []string {
	out := []string{id}
	for _, k := range f.DrawnChildren(id) {
		out = append(out, f.DeleteSet(k)...)
	}
	return out
}

func (f *fakeEngine) StorageOrphans(id string) []string {
	doomed := map[string]bool{}
	for _, d := range f.DeleteSet(id) {
		doomed[d] = true
	}
	var out []string
	for n, up := range f.stored {
		if !doomed[n] && doomed[up] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeEngine) Placement(id string) (string, int64) {
	z := f.zone[id]
	if z == "" {
		z = "local"
	}
	return z, f.deadline[id]
}

func (f *fakeEngine) WriteDrawn(set map[string]string, remove []string) error {
	f.writes++
	maps.Copy(f.over, set)
	for _, id := range remove {
		delete(f.over, id)
	}
	return nil
}

func (f *fakeEngine) MoveStorage(id, under string) error {
	if f.refuse {
		return hierarchy.ErrStorageRefused
	}
	f.stored[id] = under
	return nil
}

func (f *fakeEngine) Remove(id string, _ bool, expect []string, pins map[string]string) error {
	f.removed = expect
	f.pins = pins
	return nil
}

type guardFunc func(hierarchy.Principal, hierarchy.Plan) error

func (g guardFunc) Check(_ context.Context, by hierarchy.Principal, p hierarchy.Plan) (hierarchy.Verdict, error) {
	return hierarchy.Verdict{}, g(by, p)
}

var anyone = hierarchy.Principal{Name: "user:test"}

func TestANilGuardRefusesEveryPlan(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, nil)
	_, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Promote, Node: "X", Levels: 1})
	if !errors.Is(err, hierarchy.ErrNoGuard) {
		t.Fatalf("apply with no guard = %v, want ErrNoGuard", err)
	}
	if f.writes != 0 {
		t.Fatal("a plan with no guard wrote an edge")
	}
}

func TestPromoteNamesEveryNodeItMoves(t *testing.T) {
	f := chain()
	var seen hierarchy.Plan
	h := hierarchy.New(f, guardFunc(func(_ hierarchy.Principal, p hierarchy.Plan) error { seen = p; return nil }))
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Promote, Node: "C", Levels: 2}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range seen.Moves {
		got[m.Node] = m.From + ">" + m.To
	}
	want := map[string]string{"C": "X>stump", "X": "P>C", "P": "stump>C"}
	if !maps.Equal(got, want) {
		t.Fatalf("promote C two levels moved %v, want %v", got, want)
	}
}

// G2: a hand-built plan that leaves out the climbed parent is refused.
func TestAHandBuiltPlanThatHidesTheClimbedParentIsRefused(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	p := hierarchy.Plan{Op: hierarchy.Promote, Node: "X", Levels: 1,
		Moves: []hierarchy.Move{{Node: "X", From: "P", To: "stump", Tree: hierarchy.Drawn}}}
	if _, err := h.Apply(context.Background(), anyone, p); !errors.Is(err, hierarchy.ErrPlanMismatch) {
		t.Fatalf("apply of a plan missing P = %v, want ErrPlanMismatch", err)
	}
	if f.writes != 0 {
		t.Fatal("a mismatched plan wrote an edge")
	}
}

func TestAStalePlanIsRefused(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	at := h.Version()
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Promote, Node: "C", Levels: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Promote, Node: "C", Levels: 1, IfVersion: at})
	if !errors.Is(err, hierarchy.ErrStale) {
		t.Fatalf("apply at an old version = %v, want ErrStale", err)
	}
}

func TestDrawnOnlyRewritesStorageMovesAndChecksAgain(t *testing.T) {
	f := chain()
	calls := 0
	h := hierarchy.New(f, guardFunc(func(_ hierarchy.Principal, p hierarchy.Plan) error {
		calls++
		for _, m := range p.Moves {
			if m.Tree == hierarchy.Storage {
				return hierarchy.ErrDrawnOnly
			}
		}
		return nil
	}))
	p, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "P"}, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || p.Moves[0].Tree != hierarchy.Drawn {
		t.Fatalf("calls %d, moves %+v: want a second check of the drawn plan", calls, p.Moves)
	}
	if f.stored["C"] != "X" || f.over["C"] != "P" {
		t.Fatalf("stored %q, drawn %q: want history under X, drawn under P", f.stored["C"], f.over["C"])
	}
}

func TestAStorageMoveTheStoreRefusesIsDrawn(t *testing.T) {
	f := chain()
	f.refuse = true
	h := hierarchy.New(f, hierarchytest.Open{})
	p, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{"C": "P"}, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Moves[0].Tree != hierarchy.Drawn || f.over["C"] != "P" {
		t.Fatalf("moves %+v, override %q: want the refused storage move drawn", p.Moves, f.over["C"])
	}
}

// G5: an orphan drawn under a node in another sandbox, or one with a
// nearer deadline, goes to the nearest storage ancestor that is neither.
func TestAnOrphanIsRehomedPastAnotherSandboxAndANearerDeadline(t *testing.T) {
	f := chain()
	f.stored["Y"] = "P"
	f.stored["Z"] = "Y"
	f.stored["O"] = "Z"
	f.over["O"] = "boxy"
	f.stored["boxy"] = "stump"
	f.zone["boxy"] = "judge"
	f.deadline["Y"] = 5
	f.deadline["O"] = 10
	var seen hierarchy.Plan
	h := hierarchy.New(f, guardFunc(func(_ hierarchy.Principal, p hierarchy.Plan) error { seen = p; return nil }))
	if _, err := h.Apply(context.Background(), hierarchy.System, hierarchy.Plan{Op: hierarchy.Purge, Node: "Z"}); err != nil {
		t.Fatal(err)
	}
	if len(seen.Moves) != 1 || seen.Moves[0].Node != "O" || seen.Moves[0].To != "P" || !seen.Moves[0].Rehome {
		t.Fatalf("re-homes %+v, want O under P (past boxy's sandbox and Y's nearer deadline)", seen.Moves)
	}
	if f.pins["O"] != "P" {
		t.Fatalf("the store was told to pin O under %q, want P", f.pins["O"])
	}
}

func TestAKillTakesExactlyTheDecidedSet(t *testing.T) {
	f := chain()
	h := hierarchy.New(f, hierarchytest.Open{})
	_, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Recursive: true, Delete: []string{"X", "C"}})
	if !errors.Is(err, hierarchy.ErrPlanMismatch) {
		t.Fatalf("kill -r with a stale decided set = %v, want ErrPlanMismatch", err)
	}
	if f.removed != nil {
		t.Fatal("a mismatched delete reached the store")
	}
	if _, err := h.Apply(context.Background(), anyone, hierarchy.Plan{Op: hierarchy.Kill, Node: "X", Recursive: true, Delete: []string{"X", "C", "D"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 3 {
		t.Fatalf("removed %v, want X C D", f.removed)
	}
}
