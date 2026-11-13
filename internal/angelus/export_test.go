package angelus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tool"
)

func (a *Angelus) SweepLifetimesNow() {
	if s, ok := a.Backend.(roleSeeder); ok {
		if _, ready := a.roles(); !ready {
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

func (hs *Handlers) AfterKillCheckForTest(fn func()) { hs.h.afterKillCheck = fn }

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
		if _, ready := a.roles(); !ready {
			s.SeedRoles(context.Background(), 0)
		}
	}
}

func (hs *Handlers) OnGCReap(hook func(stage, id string)) { hs.h.gcReapHook = hook }

func (hs *Handlers) RefuseSuccessionCastsForTest(fn func(role string) error) {
	hs.h.successionCastHook = fn
}

func (hs *Handlers) SweepSuccessionsAt(now time.Time) {
	hs.h.succ.now = func() time.Time { return now }
	hs.h.sweepSuccessions()
	hs.h.succ.now = time.Now
}

func (hs *Handlers) SuccessionPhase(old string) string {
	if r := hs.h.succ.of(old); r != nil {
		return r.Phase
	}
	return ""
}

func (hs *Handlers) SuccessorOf(old string) string {
	if r := hs.h.succ.of(old); r != nil {
		return r.Successor
	}
	return ""
}

func (hs *Handlers) HoldSuccessionsForTest() (letGo func()) {
	hs.h.succ.run.Lock()
	return hs.h.succ.run.Unlock
}

func (hs *Handlers) WaitSuccessionsForTest() {
	hs.h.succ.run.Lock()
	defer hs.h.succ.run.Unlock()
}

func (a *Angelus) SetJoinBoundForTest(d time.Duration) { a.joinFor = d }

func (a *Angelus) StoppingForTest() bool { return a.inflight.sealed.Load() }

func (a *Angelus) OutForTest() []string {
	a.inflight.mu.Lock()
	defer a.inflight.mu.Unlock()
	return a.inflight.names()
}

var ErrStoppingForTest = errStopping

func (a *Angelus) JoinedForTest(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return a.refusingWhenStopping(a.joined(method, next))
}

func (a *Angelus) LeaveHubForTest(id string) { a.Hubs.putIfAbsent(id, newAriaHub(id)) }

func (hs *Handlers) PoolOutfitHash(pool string) (string, error) {
	p, err := hs.h.poolOutfit(pool)
	return p.hash, err
}

func (hs *Handlers) MintLeased(ctx context.Context, pool, key string, generation uint64) (figaro, role string, err error) {
	m, err := hs.h.mintLeased(ctx, pool, key, generation)
	return m.Figaro, m.Role, err
}

func (hs *Handlers) Place(ctx context.Context, node, under string) error {
	return hs.h.place(ctx, node, under)
}

func (hs *Handlers) PoolNode(pool string) (string, bool) {
	return poolNodeOf(hs.h.angelus.Backend, pool)
}

func (s *SlotPools) BeginMint(box string) (minted func(id string), done func(), err error) {
	call, err := s.beginMint(box)
	if err != nil {
		return nil, nil, err
	}
	return call.minted, call.done, nil
}

func (hs *Handlers) KeepReleasingSlotsEvery(ctx context.Context, every time.Duration) {
	hs.h.keepReleasingSlots(ctx, every)
}

func (s *SlotPools) ForgetLease(box string) { s.forgetLease(box) }

func (hs *Handlers) ProviderIn(box, name string) (provider.Provider, error) {
	return hs.h.factoryIn(box)(name, provider.Knobs{})
}

func (hs *Handlers) ToolsAtWake(box, figaroID string) []string {
	return hs.h.toolsIn(box, figaroID, func() *tool.Registry {
		r := tool.NewRegistry()
		r.MustRegister(&tool.ReadTool{}, &tool.WriteTool{})
		return r
	}).Names()
}

func (hs *Handlers) RunToolAtWake(ctx context.Context, box, figaroID, name string, args map[string]any) (string, error) {
	t, ok := hs.h.toolsIn(box, figaroID, tool.NewRegistry).Get(name)
	if !ok {
		return "", fmt.Errorf("no tool %s at wake in %s", name, box)
	}
	out, err := t.Execute(ctx, args, nil)
	var text strings.Builder
	for _, c := range out {
		text.WriteString(c.Text)
	}
	return text.String(), err
}

func (hs *Handlers) SayAs(ctx context.Context, box, figaroID, text string) error {
	return hs.h.sayAs(ctx, box, figaroID, text)
}

func AsLeasedBox(box string) context.Context {
	return authz.WithSubject(inProcess(context.Background()), jkauthz.Subject{Principal: authz.BoxPrincipal(box)})
}

func AsLeasedBoxThrough(box string, door authz.Door) context.Context {
	return authz.WithSubject(authz.WithDoor(context.Background(), door), jkauthz.Subject{Principal: authz.BoxPrincipal(box)})
}

func (hs *Handlers) FinishSandboxLeases(ctx context.Context) {
	loaded, _ := hs.h.settings()
	hs.h.angelus.Leases.finishUnfinished(ctx, loaded)
}

func (hs *Handlers) MayRunIn(kind, id string) error { return hs.h.mayRunIn(kind, id) }
