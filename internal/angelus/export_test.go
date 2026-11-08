package angelus

import (
	"context"
	"github.com/jack-work/figaro/api/rpc"
	"time"
)

func (a *Angelus) SweepLifetimesNow() {
	if s, ok := a.Backend.(roleSeeder); ok {
		if _, ready := a.roleTargets(); !ready {
			s.SeedRoles(context.Background(), 0)
		}
	}
	a.expireTTL()
	r := a.reclaims()
	r.drain(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for !r.quiet() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func SetMaxDoorFrameForTest(t interface{ Cleanup(func()) }, n int) {
	old := maxDoorFrame
	maxDoorFrame = n
	t.Cleanup(func() { maxDoorFrame = old })
}

func (a *Angelus) ExpireTTLForBench() { a.expireTTL() }

func (a *Angelus) WaitReclaimsForBench() {
	r := a.reclaims()
	r.drain(context.Background())
	for !r.quiet() {
		time.Sleep(100 * time.Microsecond)
	}
}

func (a *Angelus) WaitPacedReclaimsForBench() {
	r := a.reclaims()
	for !r.quiet() {
		time.Sleep(time.Millisecond)
	}
}

func (a *Angelus) RetainFormsForBench(ctx context.Context) { a.retainForms(ctx, false) }

func (a *Angelus) SetReclaimBatchForBench(n int) { a.reclaims().batch = n }

func FormViewForTest(id string) *rpc.FormView { return formViewOf(id) }

func (a *Angelus) SeedRolesNow() {
	if s, ok := a.Backend.(roleSeeder); ok {
		if _, ready := a.roleTargets(); !ready {
			s.SeedRoles(context.Background(), 0)
		}
	}
}
