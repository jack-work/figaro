package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func requireCacheMatchesTree(t *testing.T, hs *Handlers, step string) {
	t.Helper()
	if d := hs.h.angelus.fanoutDrift(); len(d) != 0 {
		t.Fatalf("after %s the cache drifted from the tree: %+v", step, d)
	}
}

func stampReaped(t *testing.T, b *store.XwalBackend, id, root string) {
	t.Helper()
	raw, _ := json.Marshal(root)
	if _, err := b.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.ReapedKey: raw}, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestTheFanoutCacheFollowsTheTreeThroughEveryChange(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	other, _ := b.CreateConversation(outfit)
	requireCacheMatchesTree(t, hs, "birth")
	var kids []string
	for range 3 {
		out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, out.Alternative)
	}
	requireCacheMatchesTree(t, hs, "three forks")
	if n := hs.h.angelus.liveChildCount(top); n != 3 {
		t.Fatalf("top holds %d live children in the cache, want 3", n)
	}
	retireAria(t, b, kids[0])
	requireCacheMatchesTree(t, hs, "a retirement")
	stampReaped(t, b, kids[1], kids[1])
	requireCacheMatchesTree(t, hs, "a reap")
	stampReaped(t, b, kids[1], "")
	requireCacheMatchesTree(t, hs, "a revival")
	if err := hs.h.place(inProcess(context.Background()), kids[2], other); err != nil {
		t.Fatal(err)
	}
	requireCacheMatchesTree(t, hs, "a reparent")
	callDemote(t, hs, rpc.DemoteRequest{FigaroID: top, Prefer: kids[1]})
	requireCacheMatchesTree(t, hs, "a demote")
	if err := hs.h.RemoveAria(kids[2], false); err != nil {
		t.Fatal(err)
	}
	requireCacheMatchesTree(t, hs, "a delete")
}

func TestAReconcileRebuildsASkewedCache(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatal(err)
	}
	c := hs.h.angelus.fanoutCounts()
	c.forget(top)
	c.put(fanoutNode{id: "ghost", parent: top, live: true})
	c.setLive(top, false)
	c.mu.Lock()
	c.kids[top] = 0
	c.mu.Unlock()
	if len(hs.h.angelus.fanoutDrift()) == 0 {
		t.Fatal("the skew was not seen")
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("a cache that lost its count refused a fork: %v", err)
	}
	hs.h.angelus.reconcileFanout()
	hs.h.angelus.reconcileFanout()
	requireCacheMatchesTree(t, hs, "two reconciles")
	if n, want := hs.h.angelus.liveChildCount(top), len(hs.h.angelus.liveChildrenOf(top)); n != want {
		t.Fatalf("top holds %d live children in the cache and %d in the tree", n, want)
	}
}

func callMethod(t *testing.T, hs *Handlers, method string, req any) error {
	t.Helper()
	raw, _ := json.Marshal(req)
	_, err := hs.Map[method](inProcess(context.Background()), raw)
	return err
}

func TestAKilledAndRestoredChildStillHoldsItsSlot(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	kid, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	for round := range 3 {
		if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kid.Alternative}); err != nil {
			t.Fatalf("round %d: kill: %v", round, err)
		}
		requireCacheMatchesTree(t, hs, "a kill")
		if err := callMethod(t, hs, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: kid.Alternative}); err != nil {
			t.Fatalf("round %d: restore: %v", round, err)
		}
		requireCacheMatchesTree(t, hs, "a restore")
		if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
			t.Fatalf("round %d: a fork past max_children 1 was admitted after a kill and a restore", round)
		}
	}
}

func TestARestoreCountsAChildTheCacheHadForgotten(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	kid, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kid.Alternative}); err != nil {
		t.Fatal(err)
	}
	hs.h.angelus.fanoutCounts().forget(kid.Alternative)
	if err := callMethod(t, hs, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: kid.Alternative}); err != nil {
		t.Fatal(err)
	}
	requireCacheMatchesTree(t, hs, "a restore")
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a fork past max_children 1 was admitted: the restore did not put the child back")
	}
}
