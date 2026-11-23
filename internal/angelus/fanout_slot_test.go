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

func oneSlotParent(t *testing.T) (*Handlers, *store.XwalBackend, string, string) {
	t.Helper()
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	kid, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a live child did not hold the only slot")
	}
	return hs, b, top, kid.Alternative
}

func requireSlotFree(t *testing.T, hs *Handlers, top, why string) {
	t.Helper()
	if n := hs.h.angelus.liveChildCount(top); n != 0 {
		t.Fatalf("after %s the parent still counts %d children", why, n)
	}
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("after %s the slot was not free: %v", why, err)
	}
}

func TestAKilledChildFreesItsSlotAtOnce(t *testing.T) {
	hs, _, top, kid := oneSlotParent(t)
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kid}); err != nil {
		t.Fatal(err)
	}
	requireSlotFree(t, hs, top, "a kill")
}

func TestAChildReapedByTTLFreesItsSlot(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 1)
	kid, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	stampReaped(t, b, kid.Alternative, kid.Alternative)
	requireSlotFree(t, hs, top, "a reap")
}

func TestARetiredChildFreesItsSlot(t *testing.T) {
	hs, b, top, kid := oneSlotParent(t)
	retireAria(t, b, kid)
	requireSlotFree(t, hs, top, "a retirement")
}

func TestARestartCountsOnlyTheLiveChildren(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	var kids []string
	for range 4 {
		out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, out.Alternative)
	}
	retireAria(t, b, kids[0])
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kids[1]}); err != nil {
		t.Fatal(err)
	}
	stampReaped(t, b, kids[2], kids[2])
	a := hs.h.angelus
	c := a.fanoutCounts()
	c.reset(a.fanoutNodes(), a.fanoutRoles())
	if n := a.liveChildCount(top); n != 1 {
		t.Fatalf("after a restart the parent counts %d children, want the 1 live one", n)
	}
	setMaxChildren(t, b, top, 1)
	if over := a.fanoutOver(); len(over) != 0 {
		t.Fatalf("the over-cap listing names %+v with one live child under max_children 1", over)
	}
	setMaxChildren(t, b, top, 0)
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("max_children 0 admitted a fork")
	}
	over := a.fanoutOver()
	if len(over) != 1 || over[0].ID != top {
		t.Fatalf("the over-cap listing is %+v, want only %s", over, top)
	}
	if want := "holds 1 live children"; !strings.Contains(over[0].Over, want) {
		t.Fatalf("the listing says %q, want %q", over[0].Over, want)
	}
}

func TestAChildRetiredWithATTLFreesItsSlotBeforeItIsReaped(t *testing.T) {
	hs, b, top, kid := oneSlotParent(t)
	if _, err := b.ApplyFormPrivileged(kid, store.RetirePatch(store.RetiredManual, 0, 10*time.Minute, time.Now())); err != nil {
		t.Fatal(err)
	}
	requireSlotFree(t, hs, top, "a retirement with a ttl")
}

func TestAPurgedChildFreesItsSlotAndStaysFreeAcrossARebuild(t *testing.T) {
	hs, _, top, kid := oneSlotParent(t)
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kid}); err != nil {
		t.Fatal(err)
	}
	if err := hs.h.RemoveAria(kid, false); err != nil {
		t.Fatal(err)
	}
	a := hs.h.angelus
	requireSlotFree(t, hs, top, "a purge")
	a.fanoutCounts().reset(a.fanoutNodes(), a.fanoutRoles())
	if n := a.liveChildCount(top); n != 1 {
		t.Fatalf("after a rebuild the parent counts %d children, want only the one fork made since the purge", n)
	}
}

func TestAChildPastItsTTLFreesItsSlotBeforeTheSweepTakesIt(t *testing.T) {
	hs, b, top, kid := oneSlotParent(t)
	applyKeys(t, b, kid, map[string]any{store.SystemTTLKey: "1h"})
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("a child inside its ttl counts %d, want 1", n)
	}
	meta, err := b.Meta(kid)
	if err != nil {
		t.Fatal(err)
	}
	meta.CreatedAtMS = time.Now().Add(-2 * time.Hour).UnixMilli()
	meta.LastAccessMS = time.Now().UnixMilli()
	if err := b.SetMeta(kid, meta); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Node(kid); !ok {
		t.Fatal("the expired child left the tree before the sweep ran")
	}
	requireSlotFree(t, hs, top, "the child's ttl ran out")
	requireCacheMatchesTree(t, hs, "a ttl running out")
}

func TestUnretiringAChildIsRefusedWhileItsParentIsFull(t *testing.T) {
	hs, b, top, kid := oneSlotParent(t)
	retireAria(t, b, kid)
	other, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	unretire := form.Build(form.Snapshot{}, nil, []string{store.SystemRetiredKey})
	if _, err := ownerSet(t, hs, kid, unretire); err == nil {
		t.Fatal("unretiring past max_children 1 was admitted")
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("a refused unretire left %d children counted, want 1", n)
	}
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: other.Alternative}); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerSet(t, hs, kid, unretire); err != nil {
		t.Fatalf("unretiring into a free slot was refused: %v", err)
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("an unretired child counts %d, want 1", n)
	}
	requireCacheMatchesTree(t, hs, "an unretire")
}

func TestLengtheningARunOutTTLIsRefusedWhileItsParentIsFull(t *testing.T) {
	hs, b, top, kid := oneSlotParent(t)
	applyKeys(t, b, kid, map[string]any{store.SystemTTLKey: "1h"})
	meta, err := b.Meta(kid)
	if err != nil {
		t.Fatal(err)
	}
	meta.CreatedAtMS = time.Now().Add(-2 * time.Hour).UnixMilli()
	if err := b.SetMeta(kid, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err != nil {
		t.Fatalf("the run-out child still held the slot: %v", err)
	}
	raw, _ := json.Marshal("48h")
	longer := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: raw}, nil)
	if _, err := ownerSet(t, hs, kid, longer); err == nil {
		t.Fatal("lengthening a run-out ttl past max_children 1 was admitted")
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("a refused ttl write left %d children counted, want 1", n)
	}
}

func ownerSet(t *testing.T, hs *Handlers, id string, p form.Patch) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(rpc.SetRequest{Patch: p})
	raw, err := rpc.WithAria(raw, id)
	if err != nil {
		t.Fatal(err)
	}
	return hs.Map[rpc.MethodSet](inProcess(context.Background()), raw)
}
