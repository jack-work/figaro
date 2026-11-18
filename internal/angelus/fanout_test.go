package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func fanoutHandlers(t *testing.T) (*Handlers, *store.XwalBackend, string) {
	t.Helper()
	return demoteHandlers(t)
}

func callFork(t *testing.T, hs *Handlers, req rpc.ForkRequest) (rpc.ForkResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := hs.Map[rpc.MethodFork](inProcess(context.Background()), raw)
	if err != nil {
		return rpc.ForkResponse{}, err
	}
	return out.(rpc.ForkResponse), nil
}

func callForkAsFigaro(t *testing.T, hs *Handlers, figaro string, req rpc.ForkRequest) (rpc.ForkResponse, error) {
	t.Helper()
	raw, err := rpc.WithCaller(req, figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := hs.Map[rpc.MethodFork](inProcess(context.Background()), raw)
	if err != nil {
		return rpc.ForkResponse{}, err
	}
	return out.(rpc.ForkResponse), nil
}

func applyKeys(t *testing.T, b *store.XwalBackend, id string, kv map[string]any) {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		set[k] = raw
	}
	if _, err := b.ApplyForm(id, form.Build(snap, set, nil)); err != nil {
		t.Fatal(err)
	}
}

func fanoutPatch(n int) *form.Patch {
	raw, _ := json.Marshal(n)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.FanoutMaxChildrenKey: raw}, nil)
	return &p
}

func setMaxChildren(t *testing.T, b *store.XwalBackend, id string, n int) {
	t.Helper()
	applyKeys(t, b, id, map[string]any{form.FanoutMaxChildrenKey: n})
}

func retireAria(t *testing.T, b *store.XwalBackend, id string) {
	t.Helper()
	applyKeys(t, b, id, map[string]any{store.SystemRetiredKey: time.Now().UTC().Format(time.RFC3339)})
}

func drawnChildren(t *testing.T, hs *Handlers, id string) []string {
	t.Helper()
	return hs.h.angelus.topology().Children(id)
}

func TestForkRefusedAtTheFanoutLimit(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 2)
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("first fork: %v", err)
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("second fork: %v", err)
	}
	_, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err == nil || !strings.Contains(err.Error(), "fanout") || !strings.Contains(err.Error(), "max_children 2") {
		t.Fatalf("third fork at the limit: %v", err)
	}
	if got := len(drawnChildren(t, hs, top)); got != 2 {
		t.Fatalf("children after the refused fork = %d, want 2 (topology unchanged)", got)
	}
}

func TestForkAllowedWithRoomUnderTheFanoutLimit(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 3)
	for range 3 {
		if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
			t.Fatalf("fork under the limit: %v", err)
		}
	}
	if got := len(drawnChildren(t, hs, top)); got != 3 {
		t.Fatalf("children = %d, want 3", got)
	}
}

func TestAForkWithNoMaxChildrenOnTheParentIsUnlimited(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	for range 5 {
		if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
			t.Fatalf("fork with no limit set: %v", err)
		}
	}
}

func TestAForkIsBornAtZeroUnlessGrantedMore(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	out, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callForkAsFigaro(t, hs, out.Alternative, rpc.ForkRequest{FigaroID: out.Alternative}); err == nil {
		t.Fatal("a fresh fork forked a child of its own with no grant")
	}

	out2, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top, Patch: fanoutPatch(2)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callForkAsFigaro(t, hs, out2.Alternative, rpc.ForkRequest{FigaroID: out2.Alternative}); err != nil {
		t.Fatalf("a fork granted max_children=2 could not fork itself: %v", err)
	}
}

func TestAnUnclaimedForkCarriesNoFanoutDefault(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: out.Alternative}); err != nil {
		t.Fatalf("a fork with no figaro claim was capped by a default: %v", err)
	}
}

// sibling is drawn under top but was not forked from it, so the demote is
// no swap and sibling's own 0 governs.
func TestAReparentPastTheLimitIsRefusedWithTopologyUnchanged(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	_, cousin, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	sibling, _ := b.CreateConversation(outfit)
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{sibling: top}, nil); err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, sibling, 0)

	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: top, Prefer: sibling})
	_, err = hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw)
	if err == nil || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("demote that overfills sibling: %v", err)
	}
	if got := drawnChildren(t, hs, top); len(got) != 2 {
		t.Fatalf("top's children after the refused demote = %v, want cousin and sibling untouched", got)
	}
	if got := len(drawnChildren(t, hs, sibling)); got != 0 {
		t.Fatalf("sibling's children after the refused demote = %d, want 0", got)
	}
	_ = cousin
}

func TestDemoteAllowsAFullPredecessorsSwap(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	grandparent, _ := b.CreateConversation(outfit)
	_, old, err := b.Fork(grandparent)
	if err != nil {
		t.Fatal(err)
	}
	_, successor, err := b.Fork(old)
	if err != nil {
		t.Fatal(err)
	}
	// old is "full": its own max_children is 1, and it already holds successor.
	setMaxChildren(t, b, old, 1)
	// grandparent, which loses old and gains successor, is also at its cap: the
	// swap is net zero there regardless.
	setMaxChildren(t, b, grandparent, 1)

	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: old, Prefer: successor})
	out, err := hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw)
	if err != nil {
		t.Fatalf("succeed's own swap was refused: %v", err)
	}
	resp := out.(rpc.DemoteResponse)
	if resp.Under != successor {
		t.Fatalf("demote under = %q, want %s", resp.Under, successor)
	}
	if got, _ := b.Node(successor); got.Present != grandparent {
		t.Fatalf("successor now drawn under %q, want %s", got.Present, grandparent)
	}
	if got, _ := b.Node(old); got.Present != successor {
		t.Fatalf("old now drawn under %q, want %s", got.Present, successor)
	}
}

func TestARetiredChildFreesAFanoutSlot(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a second fork at max_children=1 with one live child was allowed")
	}
	retireAria(t, b, out.Alternative)
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("a retired child never freed the slot: %v", err)
	}
}

func setAsFigaro(t *testing.T, hs *Handlers, figaro, target string, p form.Patch) error {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.SetRequest{Patch: p}, figaro, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = rpc.WithAria(raw, target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hs.Map[rpc.MethodSet](inProcess(context.Background()), raw)
	return err
}

func TestFanoutMaxChildrenMustBeANonNegativeInteger(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	for _, bad := range []any{-1, "two", 1.5} {
		raw, _ := json.Marshal(bad)
		p := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.FanoutMaxChildrenKey: raw}, nil)
		_, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top, Patch: &p})
		if err == nil || !strings.Contains(err.Error(), form.FanoutMaxChildrenKey) {
			t.Errorf("max_children %v: %v", bad, err)
		}
	}
}

func fanoutRemovalPatch() *form.Patch {
	p := form.Build(form.Snapshot{}, nil, []string{form.FanoutMaxChildrenKey})
	return &p
}

// An unboxed figaro held to a limit cannot grant its fork more than it
// holds, through a trusted door (the local socket, in-process), even
// though no box can verify the label: the daemon trusts it and refuses on
// it regardless.
func TestAnUnboxedFigaroCannotGrantItsForkMoreThanItHolds(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top, Patch: fanoutPatch(50)}); err == nil ||
		!strings.Contains(err.Error(), "grants at most its own max_children") {
		t.Fatalf("an unboxed figaro at max 1 forked a child granted 50: %v", err)
	}
	if got := len(drawnChildren(t, hs, top)); got != 0 {
		t.Fatalf("children after the refused fork = %d, want 0", got)
	}
}

// A claimed fork whose patch removes the key is born at the 0 default,
// the same as one that names nothing: -D never yields an unlimited child,
// whether or not the forking figaro is capped. The owner, unclaimed, may.
func TestAClaimedForkThatRemovesTheKeyIsBornAtZero(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	capped, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, capped, 3)
	uncapped, _ := b.CreateConversation(outfit)
	anc, _ := b.CreateConversation(outfit)
	_, kid, err := b.Fork(anc)
	if err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, kid, 3)
	for _, c := range []struct{ name, by, of string }{
		{"a capped figaro forking itself", capped, capped},
		{"an uncapped figaro forking itself", uncapped, uncapped},
		{"an uncapped ancestor forking a capped child", anc, kid},
	} {
		out, err := callForkAsFigaro(t, hs, c.by, rpc.ForkRequest{FigaroID: c.of, Patch: fanoutRemovalPatch()})
		if err != nil {
			t.Fatalf("%s with -D: %v", c.name, err)
		}
		if v, ok := maxChildrenOnBackend(t, b, out.Alternative); !ok || v != "0" {
			t.Fatalf("%s with -D: child max_children = %s, present=%v, want 0", c.name, v, ok)
		}
	}
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: kid, Patch: fanoutRemovalPatch()})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := maxChildrenOnBackend(t, b, out.Alternative); ok {
		t.Fatalf("the unclaimed owner's -D fork still carries max_children %s", v)
	}
}

func maxChildrenOnBackend(t *testing.T, b *store.XwalBackend, id string) (string, bool) {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := snap.Get(form.FanoutMaxChildrenKey)
	return string(raw), ok
}

// An ancestor removing a descendant's key is the same grant as any other
// value beyond its own: refused unless the ancestor is itself unlimited.
func TestAnUnboxedAncestorCannotRemoveADescendantsFanoutCapBeyondItsOwn(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	grandparent, _ := b.CreateConversation(outfit)
	_, parent, err := b.Fork(grandparent)
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := b.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, parent, 1)
	setMaxChildren(t, b, child, 1)
	if err := setAsFigaro(t, hs, parent, child, *fanoutRemovalPatch()); err == nil ||
		!strings.Contains(err.Error(), "removing it grants unlimited") {
		t.Fatalf("a capped ancestor removed a descendant's cap: %v", err)
	}
	if v, ok := maxChildrenOnBackend(t, b, child); !ok || v != "1" {
		t.Fatalf("child's max_children after the refused removal = %s, present=%v, want 1", v, ok)
	}

	// grandparent carries no key of its own: unlimited, so it may remove.
	if err := setAsFigaro(t, hs, grandparent, child, *fanoutRemovalPatch()); err != nil {
		t.Fatalf("an unlimited ancestor refused to remove a descendant's cap: %v", err)
	}
	if _, ok := maxChildrenOnBackend(t, b, child); ok {
		t.Fatalf("child still carries a max_children key after the unlimited ancestor's removal")
	}
}

// An unboxed figaro writing a target it is not an ancestor of (through its
// birth lineage) is refused even when the value would fit under its own
// cap: the label is trusted for identity, not for standing.
func TestAnUnboxedFigaroCannotGrantFanoutToANonDescendant(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	a, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, a, 5)
	other, _ := b.CreateConversation(outfit)
	if err := setAsFigaro(t, hs, a, other, fanoutPatchValue(2)); err == nil ||
		!strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a figaro granted fanout to an aria it never forked: %v", err)
	}
}

func fanoutPatchValue(n int) form.Patch {
	return *fanoutPatch(n)
}

func swapUnder(t *testing.T, hs *Handlers, by, pred, succ string) error {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.DemoteRequest{FigaroID: pred, Prefer: succ}, by, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw)
	return err
}

// A claimed self-fork is born at 0 like any other claimed fork: a capped
// figaro cannot grow its subtree by forking itself.
func TestAClaimedSelfForkIsBornAtZero(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	pred, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, pred, 2)
	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := maxChildrenOnBackend(t, b, out.Alternative); !ok || v != "0" {
		t.Fatalf("self-fork max_children = %s, present=%v, want 0", v, ok)
	}
	if _, err := callForkAsFigaro(t, hs, out.Alternative, rpc.ForkRequest{FigaroID: out.Alternative}); err == nil {
		t.Fatal("a capped figaro's self-fork forked again: the subtree grows without bound")
	}
}

// Demoting a figaro under its own fork swaps their places, and the limit
// goes with the place: the successor takes the predecessor's value and the
// predecessor takes the successor's.
func TestASwapMovesThePredecessorsLimitToItsSuccessor(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	gp, _ := b.CreateConversation(outfit)
	_, pred, err := b.Fork(gp)
	if err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, pred, 2)
	if _, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred}); err != nil {
		t.Fatal(err)
	}
	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative
	if err := swapUnder(t, hs, pred, pred, succ); err != nil {
		t.Fatalf("a capped predecessor at its limit could not demote under its own fork: %v", err)
	}
	if v, _ := maxChildrenOnBackend(t, b, succ); v != "2" {
		t.Fatalf("successor max_children after the swap = %s, want 2", v)
	}
	if v, _ := maxChildrenOnBackend(t, b, pred); v != "0" {
		t.Fatalf("predecessor max_children after the swap = %s, want 0", v)
	}
	if got := len(drawnChildren(t, hs, succ)); got != 2 {
		t.Fatalf("successor children after the swap = %d, want 2", got)
	}
	if _, err := callForkAsFigaro(t, hs, succ, rpc.ForkRequest{FigaroID: succ}); err == nil {
		t.Fatal("the successor forked past the limit it took over")
	}
	if _, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred}); err == nil {
		t.Fatal("the demoted predecessor kept its old room")
	}
}

// The fleet's common case: an uncapped holder running succeed on itself.
// Its claimed successor is born at 0, and the swap must still go through,
// leaving the successor uncapped as the predecessor was.
func TestAnUncappedHoldersClaimedSucceedCompletes(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	gp, _ := b.CreateConversation(outfit)
	_, pred, err := b.Fork(gp)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Fork(pred); err != nil {
		t.Fatal(err)
	}
	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative
	if err := swapUnder(t, hs, pred, pred, succ); err != nil {
		t.Fatalf("an uncapped holder's succeed stopped at demote: %v", err)
	}
	if v, ok := maxChildrenOnBackend(t, b, succ); ok {
		t.Fatalf("the successor of an uncapped holder is capped at %s", v)
	}
	if v, _ := maxChildrenOnBackend(t, b, pred); v != "0" {
		t.Fatalf("predecessor max_children after the swap = %s, want 0", v)
	}
}

// A succession run by another figaro (an ancestor forking the holder)
// completes the same way.
func TestASuccessionRunByAnotherFigaroCompletes(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	lead, _ := b.CreateConversation(outfit)
	_, pred, err := b.Fork(lead)
	if err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, pred, 1)
	out, err := callForkAsFigaro(t, hs, lead, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative
	if err := swapUnder(t, hs, lead, pred, succ); err != nil {
		t.Fatalf("a succession run by the lead stopped at demote: %v", err)
	}
	if v, _ := maxChildrenOnBackend(t, b, succ); v != "1" {
		t.Fatalf("successor max_children = %s, want 1", v)
	}
}

// Only a fork takes the place's limit. A child that is not a fork of the
// demoted figaro is checked against its own value and keeps it.
func TestADemoteUnderANonForkMovesNoLimit(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	gp, _ := b.CreateConversation(outfit)
	_, pred, err := b.Fork(gp)
	if err != nil {
		t.Fatal(err)
	}
	setMaxChildren(t, b, pred, 2)
	stranger, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, stranger, 0)
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{stranger: pred}, nil); err != nil {
		t.Fatal(err)
	}
	err = swapUnder(t, hs, "", pred, stranger)
	if err == nil || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("a non-fork at 0 took the predecessor's place and limit: %v", err)
	}
	if v, _ := maxChildrenOnBackend(t, b, stranger); v != "0" {
		t.Fatalf("stranger max_children = %s, want 0 unchanged", v)
	}
}

// fanoutLimit also reads a role form's own max_children: whoever holds
// the role is bound by the tighter of its own value and the role's, so
// the limit survives a succession even when the new holder's personal
// form carries none at all.
func TestFanoutLimitOfIsTheTighterOfTheHoldersOwnAndEveryRoleItHolds(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	grandparent, _ := b.CreateConversation(outfit)
	_, holder, err := b.Fork(grandparent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Fork(grandparent); err != nil {
		t.Fatal(err)
	}

	targetRaw, _ := json.Marshal(holder)
	roleMax, _ := json.Marshal(1)
	role := mintForm(t, hs.h, "target-aria", string(targetRaw), form.FanoutMaxChildrenKey, string(roleMax))
	if max, ok, err := hs.h.angelus.fanoutLimit(holder); err != nil || !ok || max != 1 {
		t.Fatalf("fanoutLimit(holder) = %d, ok=%v, err=%v, want 1 from the role %s (holder's own form carries no key)", max, ok, err, role)
	}

	// grandparent has two children (holder and its sibling); demoting
	// grandparent under holder hands holder both the sibling and
	// grandparent itself in one move: 2 arrivals, past the role's limit of
	// 1, even though holder's own form carries no cap at all.
	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: grandparent, Prefer: holder})
	_, err = hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw)
	if err == nil || !strings.Contains(err.Error(), "fanout") {
		t.Fatalf("holder's role caps it at 1, but a demote landing 2 arrivals was allowed: %v", err)
	}
	if got := len(drawnChildren(t, hs, holder)); got != 0 {
		t.Fatalf("holder's children after the refused demote = %d, want 0", got)
	}
}

func TestConcurrentForksNeverExceedTheFanoutLimit(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 2 {
		t.Fatalf("forks admitted under 8 concurrent attempts at max_children 2 = %d, want 2", ok)
	}
	if got := len(drawnChildren(t, hs, top)); got != 2 {
		t.Fatalf("children drawn after the race = %d, want 2", got)
	}
}

// The role default applies where a figaro fans out, not only on reparent:
// at fork admission and as the granter's bound.
func TestARoleCapsForkAdmissionAndTheGrant(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	holder, _ := b.CreateConversation(outfit)
	target, _ := json.Marshal(holder)
	mintForm(t, hs.h, "target-aria", string(target), form.FanoutMaxChildrenKey, "1")
	if _, err := callForkAsFigaro(t, hs, holder, rpc.ForkRequest{FigaroID: holder, Patch: fanoutPatch(50)}); err == nil ||
		!strings.Contains(err.Error(), "grants at most its own max_children (1)") {
		t.Fatalf("a holder capped at 1 by its role granted its fork 50: %v", err)
	}
	if _, err := callForkAsFigaro(t, hs, holder, rpc.ForkRequest{FigaroID: holder}); err != nil {
		t.Fatalf("first fork under the role's cap: %v", err)
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: holder}); err == nil || !strings.Contains(err.Error(), "max_children 1") {
		t.Fatalf("a second fork past the role's cap of 1: %v", err)
	}
	if got := len(drawnChildren(t, hs, holder)); got != 1 {
		t.Fatalf("children under the role's cap = %d, want 1", got)
	}
}

// On the trusted door a label only narrows: a labeled figaro writes the
// self-class keys its scopes reach, and a protected key only below itself.
func TestALabelOnlyNarrowsTheOwnersDoor(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	x, _ := b.CreateConversation(outfit)
	y, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, x, 1)
	for key, val := range map[string]string{"mantra": `"hi"`, "system.fanout.other": `"x"`} {
		p := form.Build(form.Snapshot{}, map[string]json.RawMessage{key: json.RawMessage(val)}, nil)
		if err := setAsFigaro(t, hs, x, y, p); err != nil {
			t.Fatalf("labeled write of %s on an unrelated aria: %v", key, err)
		}
	}
	scopes := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: json.RawMessage(`["read:*"]`)}, nil)
	if err := setAsFigaro(t, hs, x, y, scopes); err == nil || !strings.Contains(err.Error(), "no ancestor") {
		t.Fatalf("a labeled figaro set system.scopes on an unrelated aria: %v", err)
	}
	if err := setAsFigaro(t, hs, x, y, *fanoutPatch(0)); err == nil {
		t.Fatal("a labeled figaro set max_children on an unrelated aria")
	}
	removeAll := form.Build(form.Snapshot{}, nil, []string{"system.fanout"})
	if err := setAsFigaro(t, hs, x, x, removeAll); err == nil {
		t.Fatal("a labeled figaro removed its own system.fanout object")
	}
	if v, _ := maxChildrenOnBackend(t, b, x); v != "1" {
		t.Fatalf("x max_children = %s, want 1", v)
	}
}

// A fork racing a demote into the same parent: each fits alone, both
// together would overfill. p is no fork of g, so the demote moves no
// limit and p's own 2 holds. The shared lock admits at most the limit.
func TestAForkRacingADemoteNeverOverfills(t *testing.T) {
	for i := range 40 {
		hs, b, outfit := fanoutHandlers(t)
		g, _ := b.CreateConversation(outfit)
		p, _ := b.CreateConversation(outfit)
		if err := b.HierarchyEngine().WriteDrawn(map[string]string{p: g}, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.Fork(p); err != nil {
			t.Fatal(err)
		}
		setMaxChildren(t, b, p, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = callFork(t, hs, rpc.ForkRequest{FigaroID: p}) }()
		go func() { defer wg.Done(); _ = swapUnder(t, hs, "", g, p) }()
		wg.Wait()
		if n := len(hs.h.angelus.liveChildrenOf(p)); n > 2 {
			t.Fatalf("round %d: %d live children under max_children 2", i, n)
		}
	}
}

// Fork admission holds the topology lock across the birth: concurrent
// forks, demotes and grants under one parent must all finish.
func TestForkAdmissionDoesNotDeadlockAgainstTopologyOrGrants(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1000)
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := range 24 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
				if err != nil {
					return
				}
				kid := out.Alternative
				switch i % 3 {
				case 0:
					_ = swapUnder(t, hs, "", kid, "")
				case 1:
					_, _ = callForkAsFigaro(t, hs, kid, rpc.ForkRequest{FigaroID: kid})
				default:
					_, _ = callFork(t, hs, rpc.ForkRequest{FigaroID: kid})
					_ = setAsFigaro(t, hs, top, kid, *fanoutPatch(0))
				}
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("forks, demotes, casts and grants under one parent did not finish in 60s")
	}
}

// Mint goes with the seat. A role carrying it licenses whoever holds the
// role now, and only them; a holder's own mint moves to its successor in
// the succeed swap, and its claimed fork is born without it.
func TestSuccessionKeepsMintThroughTheRoleAndTheSwap(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	gp, _ := b.CreateConversation(outfit)
	_, pred, err := b.Fork(gp)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := json.Marshal(pred)
	role := mintForm(t, hs.h, "target-aria", string(target), form.FanoutMintKey, "true")
	applyKeys(t, b, pred, map[string]any{form.FanoutMintKey: true})
	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative
	if v, ok := keyOnBackend(t, b, succ, form.FanoutMintKey); ok {
		t.Fatalf("the claimed successor was born with mint %s", v)
	}
	if err := swapUnder(t, hs, pred, pred, succ); err != nil {
		t.Fatal(err)
	}
	if v, _ := keyOnBackend(t, b, succ, form.FanoutMintKey); v != "true" {
		t.Fatalf("successor's own mint after the swap = %q, want true", v)
	}
	if v, ok := keyOnBackend(t, b, pred, form.FanoutMintKey); ok {
		t.Fatalf("predecessor kept its own mint %s after the swap", v)
	}
	if ok, _ := hs.h.angelus.mayMint(pred); !ok {
		t.Fatal("control: the predecessor still holds the role, so it may mint")
	}
	applyKeys(t, b, succ, map[string]any{form.FanoutMintKey: false})
	succRaw, _ := json.Marshal(succ)
	if _, err := b.ApplyForm(role, form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": succRaw}, nil)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := hs.h.angelus.mayMint(succ); !ok {
		t.Fatal("the new holder of a role carrying mint may not mint")
	}
	if ok, _ := hs.h.angelus.mayMint(pred); ok {
		t.Fatal("the old holder kept mint after the role moved on")
	}
}

func keyOnBackend(t *testing.T, b *store.XwalBackend, id, key string) (string, bool) {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := snap.Get(key)
	return string(v), ok
}

// A limit the guard cannot read refuses the reparent rather than waving it
// through as unlimited.
func TestAnUnreadableLimitRefusesTheReparent(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	other, _ := b.CreateConversation(outfit)
	applyKeys(t, b, top, map[string]any{form.FanoutMaxChildrenKey: "lots"})

	raw, _ := json.Marshal(rpc.PlaceRequest{FigaroID: other, Under: top})
	_, err := hs.Map[rpc.MethodPlace](inProcess(context.Background()), raw)
	if err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("placing under a figaro whose max_children is unreadable = %v, want refused", err)
	}
	if got := drawnChildren(t, hs, top); len(got) != 0 {
		t.Fatalf("top's children after the refusal = %v, want none", got)
	}
}
