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

func TestAReparentPastTheLimitIsRefusedWithTopologyUnchanged(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	_, cousin, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	_, sibling, err := b.Fork(top)
	if err != nil {
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
	// old is "full": its own max_children is 0, and it already holds successor.
	setMaxChildren(t, b, old, 0)
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

// Removing the key grants unlimited, the same as any other value beyond
// the writer's own: a capped, unboxed figaro cannot do it to its own key
// by forking with -D, but an unlimited one may (there is nothing to
// exceed).
func TestAnUnboxedFigaroCannotRemoveItsOwnFanoutCapByForking(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	out, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top, Patch: fanoutRemovalPatch()})
	if err == nil || !strings.Contains(err.Error(), "removing it grants unlimited") {
		t.Fatalf("a capped figaro forked itself away from its own cap with -D: %v, child %s", err, out.Alternative)
	}

	unlimited, _ := b.CreateConversation(outfit)
	out, err = callForkAsFigaro(t, hs, unlimited, rpc.ForkRequest{FigaroID: unlimited, Patch: fanoutRemovalPatch()})
	if err != nil {
		t.Fatalf("an unlimited figaro refused a no-op -D on its own fork: %v", err)
	}
	if v, ok := maxChildrenOnBackend(t, b, out.Alternative); ok {
		t.Fatalf("an unlimited figaro's fork with a harmless -D still got a key: %s", v)
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

// A figaro forking itself inherits its own current limit instead of the
// usual 0 default: this is what lets it demote under its own successor
// (a succession's fork-then-demote swap) without a separate grant, while
// still leaving the successor some room of its own.
func TestASelfForkInheritsItsOwnFanoutLimitForASuccessionSwap(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	pred, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, pred, 2)

	out, err := callForkAsFigaro(t, hs, pred, rpc.ForkRequest{FigaroID: pred})
	if err != nil {
		t.Fatalf("predecessor forking itself: %v", err)
	}
	succ := out.Alternative
	if v, ok := maxChildrenOnBackend(t, b, succ); !ok || v != "2" {
		t.Fatalf("successor max_children = %s, present=%v, want 2 (inherited)", v, ok)
	}

	raw, _ := json.Marshal(rpc.DemoteRequest{FigaroID: pred, Prefer: succ})
	if _, err := hs.Map[rpc.MethodDemote](inProcess(context.Background()), raw); err != nil {
		t.Fatalf("a claimed successor could not absorb its own predecessor: %v", err)
	}
	if got := len(drawnChildren(t, hs, succ)); got != 1 {
		t.Fatalf("successor's live children after the swap = %d, want 1 (the demoted predecessor)", got)
	}

	if _, err := callForkAsFigaro(t, hs, succ, rpc.ForkRequest{FigaroID: succ}); err != nil {
		t.Fatalf("a successor with room left under its inherited limit could not fork: %v", err)
	}
	if _, err := callForkAsFigaro(t, hs, succ, rpc.ForkRequest{FigaroID: succ}); err == nil {
		t.Fatal("a successor at its inherited limit forked past it")
	}
}

// fanoutLimitOf also reads a role form's own max_children: whoever holds
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
	if max, ok := hs.h.angelus.fanoutLimitOf(holder); !ok || max != 1 {
		t.Fatalf("fanoutLimitOf(holder) = %d, ok=%v, want 1 from the role %s (holder's own form carries no key)", max, ok, role)
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
