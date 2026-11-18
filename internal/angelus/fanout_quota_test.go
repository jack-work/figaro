package angelus

import (
	"context"
	"encoding/json"
	"strings"
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
	lead := forkOK(t, hs, top, top, quotaPatch(2))
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
		t.Fatalf("an unclaimed fork inherited its parent's quota: %+v", got)
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: lead, Patch: quotaPatch(10)}); err == nil {
		t.Fatal("a lane granted 10 under an uncapped lead escaped the king's quota of 3")
	}
	forkOK(t, hs, "", lead, quotaPatch(1))
	if got := budgetOf(hs, king); got.used != 3 {
		t.Fatalf("king uses %d, want 3: the lead (1) and its lane granted 1 (2)", got.used)
	}
	requireCacheMatchesTree(t, hs, "an uncapped lead")
}

func TestRaisingAChildsQuotaNeedsTheParentsFreeBudget(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setQuota(t, b, top, 3)
	kid := forkOK(t, hs, top, top, nil)
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(2)); err != nil {
		t.Fatalf("raising a child within the free budget: %v", err)
	}
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(3)); err == nil {
		t.Fatal("top raised its child past its own free budget")
	}
	forkOK(t, hs, kid, kid, nil)
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(0)); err == nil {
		t.Fatal("a quota was lowered below what the subtree already uses")
	}
	if err := setAsFigaro(t, hs, kid, kid, *quotaPatch(3)); err == nil {
		t.Fatal("a figaro wrote its own quota")
	}
	if err := setAsFigaro(t, hs, top, kid, *quotaPatch(1)); err != nil {
		t.Fatalf("lowering to what the subtree uses: %v", err)
	}
	if got := budgetOf(hs, top); got.used != 2 {
		t.Fatalf("top uses %d after lowering its child to 1, want 2", got.used)
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
