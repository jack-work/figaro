package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func quotaPatch(n int) *form.Patch {
	raw, _ := json.Marshal(n)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.FanoutQuotaKey: raw}, nil)
	return &p
}

func grantPatch(maxChildren, quota int) *form.Patch {
	p := form.Merge(*fanoutPatch(maxChildren), *quotaPatch(quota))
	return &p
}

func setQuota(t *testing.T, b *store.XwalBackend, id string, n int) {
	t.Helper()
	applyKeys(t, b, id, map[string]any{form.FanoutQuotaKey: n})
}

func budgetOf(hs *Handlers, id string) fanoutBudget {
	return hs.h.angelus.fanoutCounts().budget(id)
}

func forkOK(t *testing.T, hs *Handlers, by, parent string, patch *form.Patch) string {
	t.Helper()
	req := rpc.ForkRequest{FigaroID: parent, Patch: patch}
	var out rpc.ForkResponse
	var err error
	if by == "" {
		out, err = callFork(t, hs, req)
	} else {
		out, err = callForkAsFigaro(t, hs, by, req)
	}
	if err != nil {
		t.Fatalf("fork of %s: %v", parent, err)
	}
	return out.Alternative
}

func TestAForkPastTheQuotaIsRefusedAndNamesTheFreeBudget(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 2)
	forkOK(t, hs, top, top, nil)
	forkOK(t, hs, top, top, nil)
	_, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err == nil {
		t.Fatal("a third fork fit a quota of 2")
	}
	if !strings.Contains(err.Error(), "0 free of quota 2") {
		t.Fatalf("the refusal does not name the free budget: %v", err)
	}
	if grant := "figaro set --id " + shortID(top) + " system.fanout.quota 3"; !strings.Contains(err.Error(), grant) {
		t.Fatalf("the refusal does not name the parent's grant %q: %v", grant, err)
	}
	if got := budgetOf(hs, top); got.used != 2 || got.quota != 2 {
		t.Fatalf("top's budget after the refusal = %+v, want 2 of 2", got)
	}
	requireCacheMatchesTree(t, hs, "a refused fork")
}

func TestAGrantAtForkComesOutOfTheParentsFreeBudget(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 3)
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top, Patch: quotaPatch(3)}); err == nil {
		t.Fatal("a fork granted 3 fit a parent with 3 free: the child costs itself too")
	}
	lead := forkOK(t, hs, top, top, grantPatch(3, 2))
	if got := budgetOf(hs, top); got.used != 3 {
		t.Fatalf("a child granted 2 charges %d, want 3", got.used)
	}
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("top forked past its quota after granting the rest")
	}
	forkOK(t, hs, lead, lead, nil)
	forkOK(t, hs, lead, lead, nil)
	if _, err := callForkAsFigaro(t, hs, lead, rpc.ForkRequest{FigaroID: lead}); err == nil {
		t.Fatal("the lead forked past its grant")
	}
	if got := budgetOf(hs, top); got.used != 3 {
		t.Fatalf("the lead's forks reached top's budget: %+v", got)
	}
	requireCacheMatchesTree(t, hs, "grants")
}

// The fork-admission correction: an uncapped child charges 1 plus what its
// subtree uses, so a grant under an uncapped lead cannot escape the cap above.
func TestAGrantUnderAnUncappedLeadCountsAgainstTheCapAbove(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	king, _ := b.CreateConversation(outfit)
	setQuota(t, b, king, 3)
	lead := forkOK(t, hs, "", king, nil)
	if got := budgetOf(hs, lead); got.capped {
		t.Fatalf("an owner's fork inherited its parent's quota: %+v", got)
	}
	if _, err := callForkAsFigaro(t, hs, lead, rpc.ForkRequest{FigaroID: lead, Patch: quotaPatch(10)}); err == nil {
		t.Fatal("a lane granted 10 under an uncapped lead escaped the king's quota of 3")
	}
	forkOK(t, hs, lead, lead, quotaPatch(1))
	if got := budgetOf(hs, king); got.used != 3 {
		t.Fatalf("king uses %d, want 3: the lead (1) and its lane granted 1 (2)", got.used)
	}
	requireCacheMatchesTree(t, hs, "an uncapped lead")
}

func TestRaisingAChildsQuotaNeedsTheParentsFreeBudget(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 3)
	kid := forkOK(t, hs, top, top, fanoutPatch(1))
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(2)); err != nil {
		t.Fatalf("raising a child within the free budget: %v", err)
	}
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(3)); err == nil {
		t.Fatal("top raised its child past its own free budget")
	}
	grandkid := forkOK(t, hs, kid, kid, nil)
	if err := setAsFigaro(t, hs, kid, kid, *quotaPatch(3)); err == nil {
		t.Fatal("a figaro wrote its own quota")
	}
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(0)); err != nil {
		t.Fatalf("Gluck's call: lowering below use is allowed: %v", err)
	}
	if got := budgetOf(hs, top); got.used != 2 {
		t.Fatalf("top uses %d with its child lowered to 0 but using 1, want 1 + max(0, 1) = 2", got.used)
	}
	if _, err := callForkAsFigaro(t, hs, kid, rpc.ForkRequest{FigaroID: kid}); err == nil {
		t.Fatal("a child lowered below its use forked")
	}
	if !hs.h.angelus.isLiveAria(grandkid) {
		t.Fatal("lowering a quota reaped what was under it")
	}
	retireAria(t, b, grandkid)
	if got := budgetOf(hs, top); got.used != 1 {
		t.Fatalf("top uses %d once its child's use dropped, want 1", got.used)
	}
	requireCacheMatchesTree(t, hs, "re-grants")
}

func TestRetiringAChildReturnsItsBudget(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 2)
	kid := forkOK(t, hs, top, top, quotaPatch(1))
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("top forked past its quota")
	}
	retireAria(t, b, kid)
	forkOK(t, hs, top, top, nil)
	requireCacheMatchesTree(t, hs, "a retirement")
}

func TestAReparentCarriesTheSubtreesWholeCharge(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	a, _ := b.CreateConversation(outfit)
	other, _ := b.CreateConversation(outfit)
	setQuota(t, b, a, 2)
	s := forkOK(t, hs, "", other, quotaPatch(2))
	was, _ := hs.h.angelus.topology().Drawn(s)
	err := hs.h.place(inProcess(context.Background()), s, a)
	if err == nil {
		t.Fatal("a subtree charging 3 moved under a quota of 2")
	}
	if now, _ := hs.h.angelus.topology().Drawn(s); now != was {
		t.Fatalf("a refused reparent moved the subtree to %s", now)
	}
	setQuota(t, b, a, 3)
	if err := hs.h.place(inProcess(context.Background()), s, a); err != nil {
		t.Fatalf("a subtree charging 3 under a quota of 3: %v", err)
	}
	if got := budgetOf(hs, a); got.used != 3 {
		t.Fatalf("a uses %d after the move, want 3", got.used)
	}
	requireCacheMatchesTree(t, hs, "a reparent")
}

func TestARoleCeilingLowersTheHoldersQuota(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 5)
	held, _ := json.Marshal(top)
	role, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held, form.FanoutQuotaKey: json.RawMessage(`1`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := budgetOf(hs, top); got.quota != 1 {
		t.Fatalf("holder of a role capped at 1 has quota %d", got.quota)
	}
	forkOK(t, hs, top, top, nil)
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("the holder forked past its role's ceiling")
	}
	applyKeys(t, b, role, map[string]any{"target-aria": ""})
	if got := budgetOf(hs, top); got.quota != 5 {
		t.Fatalf("the ceiling stayed after the role was dropped: %+v", got)
	}
	forkOK(t, hs, top, top, nil)
	requireCacheMatchesTree(t, hs, "a role ceiling")
}

func TestTheQuotaIsShownInTheListing(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 4)
	forkOK(t, hs, top, top, quotaPatch(1))
	raw, _ := json.Marshal(rpc.ListRequest{})
	out, err := hs.Map[rpc.MethodList](inProcess(context.Background()), raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.(rpc.ListResponse).Figaros {
		if f.ID != top {
			continue
		}
		if f.FanQuota == nil || *f.FanQuota != 4 || f.FanUsed != 2 {
			t.Fatalf("top's row shows %d of %v, want 2 of 4", f.FanUsed, f.FanQuota)
		}
		return
	}
	t.Fatal("top has no row")
}

func keyIn(t *testing.T, b *store.XwalBackend, id, key string) (string, bool) {
	t.Helper()
	snap, err := b.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := snap.Get(key)
	return string(v), ok
}

// Gluck's call: the owner's forks skip every fan-out check but still count,
// and a figaro at its cap is told which of them it may retire.
func TestAnOwnerForkSkipsAdmissionButCounts(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 1)
	setMaxChildren(t, b, top, 1)
	a := forkOK(t, hs, "", top, nil)
	c := forkOK(t, hs, "", top, nil)
	if got := budgetOf(hs, top); got.used != 2 || hs.h.angelus.liveChildCount(top) != 2 {
		t.Fatalf("two owner forks were not counted: %+v, %d live", got, hs.h.angelus.liveChildCount(top))
	}
	if v, ok := keyIn(t, b, a, form.OwnerMadeKey); !ok || v != "true" {
		t.Fatalf("an owner fork carries %s = %s (present %v)", form.OwnerMadeKey, v, ok)
	}
	_, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err == nil {
		t.Fatal("a figaro over its cap forked")
	}
	setMaxChildren(t, b, top, 3)
	_, err = callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err == nil || !strings.Contains(err.Error(), "the owner made ") || !strings.Contains(err.Error(), shortID(a)) || !strings.Contains(err.Error(), shortID(c)) {
		t.Fatalf("the quota refusal does not name the owner's forks: %v", err)
	}
	for _, id := range []string{a, c} {
		if err := setAsFigaro(t, hs, top, id, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemRetiredKey: json.RawMessage(`"room"`)}, nil)); err != nil {
			t.Fatalf("a capped figaro could not retire an owner's fork under it: %v", err)
		}
	}
	forkOK(t, hs, top, top, nil)
	requireCacheMatchesTree(t, hs, "owner forks")
}

// Gluck's invariant: a fork never copies its parent's quota; a quota is
// carved from the parent's free budget, a child_policy's included.
func TestAForkNeverCopiesItsParentsQuota(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 3)
	owners := forkOK(t, hs, "", top, nil)
	if v, ok := keyIn(t, b, owners, form.FanoutQuotaKey); ok {
		t.Fatalf("an owner's fork copied the quota: %s", v)
	}
	claimedKid := forkOK(t, hs, top, top, nil)
	if v, _ := keyIn(t, b, claimedKid, form.FanoutQuotaKey); v != "0" {
		t.Fatalf("a claimed fork's quota = %q, want 0", v)
	}
	applyKeys(t, b, top, map[string]any{form.ChildPolicyKey: map[string]any{form.FanoutQuotaKey: 1}})
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a child_policy quota of 1 was not carved: top had 1 free and the child costs 2")
	}
	retireAria(t, b, owners)
	carved := forkOK(t, hs, top, top, nil)
	if v, _ := keyIn(t, b, carved, form.FanoutQuotaKey); v != "1" {
		t.Fatalf("the child_policy quota = %q, want 1", v)
	}
	if got := budgetOf(hs, top); got.used != 3 {
		t.Fatalf("top uses %d, want 3: a claimed child (1) and a carved one (2)", got.used)
	}
	requireCacheMatchesTree(t, hs, "carves")
}

// Gluck's call: admission is optimistic. Forks and quota writes read the
// counts without the hierarchy lock, so a race may overshoot, but only by
// what was in flight, and the counts converge on the tree: an overdrawn
// figaro is reported and refused every fork until it is back under.
func TestRacingForksAndQuotaWritesOvershootBoundedlyAndConverge(t *testing.T) {
	const quota, raisers, step = 6, 6, 3
	for round := range 10 {
		hs, b, outfit := fanoutHandlers(t)
		top, _ := b.CreateConversation(outfit)
		setQuota(t, b, top, quota)
		kids := []string{forkOK(t, hs, top, top, nil), forkOK(t, hs, top, top, nil)}
		var wg sync.WaitGroup
		for i := range 2 * raisers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%2 == 0 {
					_, _ = callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
					return
				}
				_ = setAsFigaro(t, hs, top, kids[i%4/2], *quotaPatch(1 + i%step))
			}()
		}
		wg.Wait()
		used := budgetOf(hs, top).used
		if used > quota+raisers*step {
			t.Fatalf("round %d: top uses %d, past its quota %d plus everything in flight", round, used, quota)
		}
		hs.h.angelus.reconcileFanout()
		requireCacheMatchesTree(t, hs, fmt.Sprintf("round %d", round))
		over := false
		for _, o := range hs.h.angelus.fanoutOver() {
			over = over || o.ID == top
		}
		if over != (used > quota) {
			t.Fatalf("round %d: top uses %d of %d and is reported over budget: %v", round, used, quota, over)
		}
		if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); (err == nil) != (used < quota) {
			t.Fatalf("round %d: top at %d of %d forked: %v", round, used, quota, err)
		}
	}
}

// Defends M2 of the P6 review: forks no longer wait on the hierarchy lock,
// so the quota charge must be held with the slot. N concurrent forks
// against a quota of K admit exactly K.
func TestConcurrentForksAgainstAQuotaAdmitExactlyIt(t *testing.T) {
	const quota, forks = 5, 16
	for round := range 20 {
		hs, b, outfit := fanoutHandlers(t)
		top, _ := b.CreateConversation(outfit)
		setQuota(t, b, top, quota)
		start := make(chan struct{})
		var landed atomic.Int32
		var wg sync.WaitGroup
		for range forks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
					landed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := landed.Load(); got != quota {
			t.Fatalf("round %d: %d of %d concurrent forks landed against quota %d", round, got, forks, quota)
		}
		if got := budgetOf(hs, top); got.used != quota {
			t.Fatalf("round %d: top uses %d, want %d", round, got.used, quota)
		}
		requireCacheMatchesTree(t, hs, "concurrent forks against a quota")
	}
}
