package admission

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
)

// oracle recomputes every counter from parent edges alone, so a test can
// prove the incremental bookkeeping never drifts from a fresh rebuild.
func oracle(t *testing.T, l *Ledger) {
	t.Helper()
	got := l.snapshot()
	nodes := make([]Node, 0, len(got))
	for id, e := range got {
		nodes = append(nodes, Node{ID: id, Parent: e.parent, Live: e.live, Limits: e.limits})
	}
	fresh := New()
	fresh.Rebuild(nodes)
	want := fresh.snapshot()
	for id, w := range want {
		g := got[id]
		if g.kids != w.kids || g.used != w.used || g.nkids != w.nkids {
			t.Fatalf("%s drifted: kids %d used %d nkids %d, a rebuild says %d %d %d", id, g.kids, g.used, g.nkids, w.kids, w.used, w.nkids)
		}
		if w.limits.HasQuota && w.used > w.limits.Quota {
			t.Fatalf("%s uses %d of quota %d", id, w.used, w.limits.Quota)
		}
		if w.limits.HasMaxChildren && w.kids > w.limits.MaxChildren {
			t.Fatalf("%s holds %d live children of max %d", id, w.kids, w.limits.MaxChildren)
		}
	}
}

func quota(n int) Limits { return Limits{Quota: n, HasQuota: true} }

func TestMaxChildrenStaysBesideTheQuota(t *testing.T) {
	l := New()
	l.Rebuild([]Node{{ID: "king", Live: true, Limits: Limits{Quota: 10, HasQuota: true, MaxChildren: 2, HasMaxChildren: true}}})
	a, err := l.Reserve("king", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	l.Commit(a, "a")
	if _, err := l.Reserve("king", Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve("king", Limits{}); !errors.Is(err, ErrMaxChildren) {
		t.Fatalf("third child under max_children 2: %v", err)
	}
	if c, _ := l.Of("king"); c.Used != 2 {
		t.Fatalf("used = %d, want 2", c.Used)
	}
	oracle(t, l)
}

func TestAQuotaBoundsTheWholeSubtreeThroughUncappedLevels(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "king", Live: true, Limits: quota(3)},
		{ID: "lead", Parent: "king", Live: true},
		{ID: "lane", Parent: "lead", Live: true},
	})
	if _, err := l.Reserve("lane", Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve("lane", Limits{}); !errors.Is(err, ErrQuota) {
		t.Fatalf("fifth figaro under a quota of 3 at the king: %v", err)
	}
	oracle(t, l)
}

func TestAGrantChargesItsWholeQuotaAndAbsorbsBelow(t *testing.T) {
	l := New()
	l.Rebuild([]Node{{ID: "king", Live: true, Limits: quota(5)}})
	tk, err := l.Reserve("king", quota(3))
	if err != nil {
		t.Fatal(err)
	}
	l.Commit(tk, "lead")
	if c, _ := l.Of("king"); c.Used != 4 {
		t.Fatalf("king used = %d, want 4", c.Used)
	}
	for range 3 {
		if _, err := l.Reserve("lead", Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := l.Of("king"); c.Used != 4 {
		t.Fatalf("the lead's own forks reached the king: used %d", c.Used)
	}
	if _, err := l.Reserve("king", quota(1)); !errors.Is(err, ErrQuota) {
		t.Fatalf("a grant of 1 needs 2 and the king has 1 free: %v", err)
	}
	oracle(t, l)
}

func TestAnAbortedForkGivesItsChargeBack(t *testing.T) {
	l := New()
	l.Rebuild([]Node{{ID: "king", Live: true, Limits: Limits{Quota: 1, HasQuota: true, MaxChildren: 1, HasMaxChildren: true}}})
	tk, err := l.Reserve("king", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	l.Abort(tk)
	if _, err := l.Reserve("king", Limits{}); err != nil {
		t.Fatalf("after the abort: %v", err)
	}
	oracle(t, l)
}

func TestRetiringALaneFreesItsSlotButNotItsChildren(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "king", Live: true, Limits: quota(3)},
		{ID: "lane", Parent: "king", Live: true},
		{ID: "sub", Parent: "lane", Live: true},
	})
	if err := l.SetLive("lane", false); err != nil {
		t.Fatal(err)
	}
	if c, _ := l.Of("king"); c.Used != 1 || c.LiveChildren != 0 {
		t.Fatalf("after retiring the lane: %+v, want used 1, no live children", c)
	}
	if _, err := l.Reserve("king", quota(1)); err != nil {
		t.Fatal(err)
	}
	if err := l.SetLive("lane", true); !errors.Is(err, ErrQuota) {
		t.Fatalf("reviving past the quota: %v", err)
	}
	oracle(t, l)
}

func TestAMoveCarriesItsWholeChargeAndIsRefusedWhole(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "a", Live: true, Limits: quota(5)},
		{ID: "b", Live: true, Limits: quota(2)},
		{ID: "s", Parent: "a", Live: true},
		{ID: "s1", Parent: "s", Live: true},
		{ID: "s2", Parent: "s", Live: true},
	})
	if err := l.Move("s", "b"); !errors.Is(err, ErrQuota) {
		t.Fatalf("a subtree of 3 into a quota of 2: %v", err)
	}
	if c, _ := l.Of("a"); c.Used != 3 {
		t.Fatalf("a refused move changed a: used %d", c.Used)
	}
	if err := l.Move("s1", "b"); err != nil {
		t.Fatal(err)
	}
	if err := l.Move("s", "s2"); !errors.Is(err, ErrCycle) {
		t.Fatalf("a move under a descendant: %v", err)
	}
	oracle(t, l)
}

func TestLoweringAQuotaBelowUseIsRefused(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "king", Live: true, Limits: quota(10)},
		{ID: "lead", Parent: "king", Live: true, Limits: quota(4)},
		{ID: "x", Parent: "lead", Live: true},
		{ID: "y", Parent: "lead", Live: true},
	})
	if err := l.SetLimits("lead", quota(1)); !errors.Is(err, ErrQuota) {
		t.Fatalf("lowering under use: %v", err)
	}
	if err := l.SetLimits("lead", quota(2)); err != nil {
		t.Fatal(err)
	}
	if c, _ := l.Of("king"); c.Used != 3 {
		t.Fatalf("king used %d after the lead returned 2, want 3", c.Used)
	}
	if err := l.SetLimits("lead", quota(20)); !errors.Is(err, ErrQuota) {
		t.Fatalf("a raise past the king's free budget: %v", err)
	}
	oracle(t, l)
}

// Concurrent forks, retirements and moves under capped ancestors never
// overshoot a cap and never drift from a fresh rebuild.
func TestConcurrentAdmissionHoldsEveryCap(t *testing.T) {
	for round := range 20 {
		l := New()
		nodes := []Node{
			{ID: "king", Live: true, Limits: Limits{Quota: 40, HasQuota: true, MaxChildren: 6, HasMaxChildren: true}},
			{ID: "eng", Parent: "king", Live: true, Limits: Limits{Quota: 12, HasQuota: true, MaxChildren: 4, HasMaxChildren: true}},
			{ID: "ops", Parent: "king", Live: true},
		}
		l.Rebuild(nodes)
		var ids sync.Map
		for _, n := range nodes {
			ids.Store(n.ID, true)
		}
		pick := func(r *rand.Rand) string {
			var all []string
			ids.Range(func(k, _ any) bool { all = append(all, k.(string)); return true })
			return all[r.IntN(len(all))]
		}
		var seq atomic.Int64
		var topo sync.Mutex
		var wg sync.WaitGroup
		for w := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(round), uint64(w)))
				for range 400 {
					switch r.IntN(14) {
					case 0, 1, 2, 3, 4:
						grant := Limits{}
						if r.IntN(3) == 0 {
							grant = quota(r.IntN(3))
						}
						tk, err := l.Reserve(pick(r), grant)
						if err != nil {
							continue
						}
						if r.IntN(5) == 0 {
							l.Abort(tk)
							continue
						}
						id := fmt.Sprintf("n%d", seq.Add(1))
						l.Commit(tk, id)
						ids.Store(id, true)
					case 5, 6:
						topo.Lock()
						_ = l.SetLive(pick(r), r.IntN(2) == 0)
						topo.Unlock()
					case 7, 8:
						topo.Lock()
						_ = l.Move(pick(r), pick(r))
						topo.Unlock()
					case 9:
						topo.Lock()
						_ = l.SetLimits(pick(r), quota(r.IntN(6)))
						topo.Unlock()
					case 10:
						l.Mark(pick(r), false)
					case 11, 12, 13:
						topo.Lock()
						a, b, c := pick(r), pick(r), pick(r)
						s, err := l.Admit([]Arrival{{Node: a, To: b}, {Node: b, To: c}})
						if err != nil {
							topo.Unlock()
							continue
						}
						if r.IntN(3) == 0 {
							l.Settle(s, nil, nil)
						} else {
							l.Settle(s, []Placement{{ID: a, Parent: b}, {ID: b, Parent: c}}, nil)
						}
						topo.Unlock()
					}
				}
			}()
		}
		wg.Wait()
		oracle(t, l)
	}
}

func BenchmarkReserveAndCommitAtDepth(b *testing.B) {
	for _, depth := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			l := New()
			nodes := []Node{{ID: "n0", Live: true, Limits: quota(1 << 30)}}
			for i := 1; i < depth; i++ {
				nodes = append(nodes, Node{ID: fmt.Sprintf("n%d", i), Parent: fmt.Sprintf("n%d", i-1), Live: true})
			}
			l.Rebuild(nodes)
			leaf := fmt.Sprintf("n%d", depth-1)
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				tk, err := l.Reserve(leaf, Limits{})
				if err != nil {
					b.Fatal(err)
				}
				l.Abort(tk)
			}
		})
	}
}

func BenchmarkParallelReserveUnderOneCap(b *testing.B) {
	l := New()
	l.Rebuild([]Node{
		{ID: "king", Live: true, Limits: quota(1 << 30)},
		{ID: "lead", Parent: "king", Live: true},
	})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tk, err := l.Reserve("lead", Limits{})
			if err != nil {
				b.Fatal(err)
			}
			l.Abort(tk)
		}
	})
}

func BenchmarkRebuild(b *testing.B) {
	r := rand.New(rand.NewPCG(1, 2))
	nodes := make([]Node, 2543)
	for i := range nodes {
		parent := ""
		if i > 0 && r.IntN(4) != 0 {
			parent = fmt.Sprintf("n%d", r.IntN(i))
		}
		nodes[i] = Node{ID: fmt.Sprintf("n%d", i), Parent: parent, Live: r.IntN(3) != 0}
	}
	for b.Loop() {
		New().Rebuild(nodes)
	}
}

func capped(n int) Limits { return Limits{MaxChildren: n, HasMaxChildren: true} }

func TestExactlyTheCapOfConcurrentForksIsAdmitted(t *testing.T) {
	for range 50 {
		l := New()
		l.Rebuild([]Node{{ID: "p", Live: true, Limits: capped(4)}})
		var ok atomic.Int64
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tk, err := l.Reserve("p", Limits{})
				if err != nil {
					return
				}
				ok.Add(1)
				l.Commit(tk, fmt.Sprintf("c%d", i))
			}()
		}
		wg.Wait()
		if ok.Load() != 4 {
			t.Fatalf("%d forks admitted under max_children 4", ok.Load())
		}
		oracle(t, l)
	}
}

func TestAShiftCreditsDeparturesAndRefusesWhole(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "p", Live: true, Limits: capped(2)},
		{ID: "a", Parent: "p", Live: true},
		{ID: "b", Parent: "p", Live: true},
		{ID: "q", Live: true},
		{ID: "x", Parent: "q", Live: true},
		{ID: "y", Parent: "q", Live: true},
	})
	s, err := l.Admit([]Arrival{{Node: "a", To: "q"}, {Node: "x", To: "p"}})
	if err != nil {
		t.Fatalf("a swap that keeps p at 2: %v", err)
	}
	l.Settle(s, []Placement{{ID: "a", Parent: "q"}, {ID: "x", Parent: "p"}}, nil)
	if _, err := l.Admit([]Arrival{{Node: "y", To: "p"}}); !errors.Is(err, ErrMaxChildren) {
		t.Fatalf("a third live child of p: %v", err)
	}
	if c, _ := l.Of("q"); c.LiveChildren != 2 {
		t.Fatalf("the refused shift moved something: q holds %d", c.LiveChildren)
	}
	oracle(t, l)
}

func TestAnUncommittedShiftPutsEveryNodeBack(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "p", Live: true, Limits: capped(1)},
		{ID: "a", Parent: "p", Live: true},
		{ID: "q", Live: true},
	})
	s, err := l.Admit([]Arrival{{Node: "a", To: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve("p", Limits{}); !errors.Is(err, ErrMaxChildren) {
		t.Fatalf("a departure in flight freed its slot early: %v", err)
	}
	l.Settle(s, nil, nil)
	if c, _ := l.Of("p"); c.LiveChildren != 1 {
		t.Fatalf("p holds %d after the shift was dropped", c.LiveChildren)
	}
	oracle(t, l)
}

func TestSettleForgetsADeletedSubtree(t *testing.T) {
	l := New()
	l.Rebuild([]Node{
		{ID: "p", Live: true, Limits: capped(2)},
		{ID: "a", Parent: "p", Live: true},
		{ID: "a1", Parent: "a", Live: true},
		{ID: "a2", Parent: "a", Live: true},
	})
	l.Settle(nil, []Placement{{ID: "a2", Parent: "p"}}, []string{"a", "a1"})
	if c, _ := l.Of("p"); c.LiveChildren != 1 || c.Used != 1 {
		t.Fatalf("p after the delete: %+v", c)
	}
	oracle(t, l)
}

func TestAuditNamesEveryDrift(t *testing.T) {
	l := New()
	nodes := []Node{{ID: "p", Live: true}, {ID: "a", Parent: "p", Live: true}}
	l.Rebuild(nodes)
	if d := l.Audit(nodes); len(d) != 0 {
		t.Fatalf("a fresh ledger drifts: %v", d)
	}
	tk, _ := l.Reserve("p", Limits{})
	if d := l.Audit(nodes); len(d) != 0 {
		t.Fatalf("a fork in flight reads as drift: %v", d)
	}
	l.Abort(tk)
	l.Mark("a", false)
	d := l.Audit(nodes)
	if len(d) != 2 {
		t.Fatalf("a retirement the store never made: %v", d)
	}
}
