package angelus

import (
	"context"
	"encoding/json"
	"strings"
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
