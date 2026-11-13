package angelus_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/workerpool"
)

// A provider that sends the rou2 lease it was built with, as the routed
// one does.
type leasedMock struct {
	mockProviderForIntegration
	lease string
}

func (m *leasedMock) Lease() string { return m.lease }

// rou2 as the lease handlers see it: the leases this daemon opened and
// closed, in order, and a way to make either call fail.
type rigRou2 struct {
	mu        sync.Mutex
	provider  []string
	opened    []string
	classes   map[string]string
	closed    []string
	failOpen  error
	failClose error
	onOpen    func(id string)
}

func (f *rigRou2) Open(_ context.Context, id, class string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onOpen != nil {
		f.onOpen(id)
	}
	if f.failOpen != nil {
		return f.failOpen
	}
	f.opened = append(f.opened, id)
	f.classes[id] = class
	return nil
}

func (f *rigRou2) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failClose != nil {
		return f.failClose
	}
	f.closed = append(f.closed, id)
	return nil
}

func (f *rigRou2) closeFails(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failClose = err
}

var rigRou2s sync.Map

func installRou2(r grantRig) *rigRou2 {
	f := &rigRou2{classes: map[string]string{}}
	r.a.Workers.Caller = func(pool config.PoolConfig) (workerpool.Caller, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.provider = append(f.provider, pool.Provider)
		return f, nil
	}
	rigRou2s.Store(r.a, f)
	return f
}

func rou2Of(r grantRig) *rigRou2 {
	f, _ := rigRou2s.Load(r.a)
	return f.(*rigRou2)
}

func boxNodes(a *angelus.Angelus, box string) int {
	n := 0
	for _, node := range a.Backend.Nodes() {
		if snap, err := a.Backend.FormState(node.ID); err == nil {
			if s := snap.Lookup("system.sandbox"); s != nil && *s == box {
				n++
			}
		}
	}
	return n
}

// Step 5 of a lease and step 3 of a release (B4). The lease's caller at
// rou2 is `<pool>/<box>`, opened under the pool's class through the pool's
// provider after the ledger names it and before the box exists or anything
// is minted; the release closes it, once.
func TestALeaseOpensItsRou2LeaseBeforeItMintsAndItsReleaseClosesIt(t *testing.T) {
	r := leasingRig(t)
	rou2 := rou2Of(r)
	rou2.onOpen = func(id string) {
		row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1)
		if row.Rou2Lease != id || row.State != workerpool.Minting {
			t.Errorf("rou2 was asked for %s while the ledger row is %s naming %q", id, row.State, row.Rou2Lease)
		}
		if slices.Contains(r.a.Settings.LeasedBoxes(), "worker0-g1") || boxNodes(r.a, "worker0-g1") != 0 {
			t.Error("rou2 was asked after the box was leased or a node minted in it")
		}
	}
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rou2.opened, []string{"groups/worker0-g1"}) || rou2.classes["groups/worker0-g1"] != "group" || !slices.Equal(rou2.provider, []string{"mock"}) {
		t.Errorf("the lease opened %v under %v through %v, want groups/worker0-g1 under the pool's class through its provider", rou2.opened, rou2.classes, rou2.provider)
	}
	if _, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err != nil || len(rou2.opened) != 1 {
		t.Errorf("the lease answered again opened another: %v (%v)", rou2.opened, err)
	}
	if len(rou2.closed) != 0 {
		t.Errorf("a live lease's rou2 lease was closed: %v", rou2.closed)
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rou2.closed, []string{"groups/worker0-g1"}) {
		t.Errorf("the release closed %v, want groups/worker0-g1", rou2.closed)
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil || len(rou2.closed) != 1 {
		t.Errorf("a release answered again closed again: %v (%v)", rou2.closed, err)
	}
	if ariaExists(r.a, lease.Aria) {
		t.Error("the released lease's figaro is still there")
	}
}

// A lease rou2 does not open is no lease: nothing is minted, no box is
// leased, and the row is released, after a close of the id the ledger
// named, since the daemon cannot know the open did not land.
func TestALeaseRou2DoesNotOpenMintsNothingAndIsReleased(t *testing.T) {
	r := leasingRig(t)
	rou2 := rou2Of(r)
	rou2.failOpen = errors.New("rou2 is away")
	if _, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err == nil {
		t.Fatal("a lease rou2 did not open was answered")
	}
	if n := boxNodes(r.a, "worker0-g1"); n != 0 {
		t.Errorf("%d nodes were minted for a lease rou2 did not open", n)
	}
	if slices.Contains(r.a.Settings.LeasedBoxes(), "worker0-g1") {
		t.Error("the box of a lease rou2 did not open is in the table")
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Released {
		t.Errorf("the row is %s, want released", row.State)
	}
	if !slices.Equal(rou2.closed, []string{"groups/worker0-g1"}) {
		t.Errorf("the failed lease closed %v, want the id its row named", rou2.closed)
	}
	rou2.failOpen = nil
	if next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err != nil || next.Box != "worker0-g2" {
		t.Errorf("after the failure the key leases %+v (%v), want worker0 at generation 2", next, err)
	}
}

// A release whose rou2 lease does not close still purges, and is not done:
// the row stays releasing and the worker is not offered, because the caller
// can still spend. The same release asked again finishes it.
func TestAReleaseRou2DoesNotCloseStillPurgesAndKeepsTheWorker(t *testing.T) {
	r := leasingRig(t)
	rou2 := rou2Of(r)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	rou2.failClose = errors.New("rou2 is away")
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("a release whose rou2 lease did not close answered released")
	}
	if ariaExists(r.a, lease.Aria) || ariaExists(r.a, lease.Role) {
		t.Error("the lease's figaro or role outlived a release that could not close its rou2 lease")
	}
	if slices.Contains(r.a.Settings.LeasedBoxes(), lease.Box) {
		t.Error("the box is still in the table")
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Releasing || row.Rou2Lease != "groups/worker0-g1" {
		t.Errorf("the row is %s naming %q, want releasing and still naming its rou2 lease", row.State, row.Rou2Lease)
	}
	if next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`); err != nil || next.Box != "worker1-g1" {
		t.Errorf("with worker0 unclosed the next lease is %+v (%v), want worker1", next, err)
	}
	rou2.failClose = nil
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Released || !slices.Equal(rou2.closed, []string{"groups/worker0-g1"}) {
		t.Errorf("after the second release the row is %s and rou2 closed %v", row.State, rou2.closed)
	}
}

// A daemon that reaches no rou2 leases nothing, and a lease that never
// named a rou2 lease is released without one.
func TestWithNoRou2NothingIsLeased(t *testing.T) {
	r := leasingRig(t)
	r.a.Workers.Caller = nil
	if _, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err == nil {
		t.Fatal("a daemon with no rou2 answered a lease")
	}
	if n := boxNodes(r.a, "worker0-g1"); n != 0 {
		t.Errorf("%d nodes were minted with no rou2", n)
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Released || row.Rou2Lease != "" {
		t.Errorf("the row is %s naming %q, want released and naming none", row.State, row.Rou2Lease)
	}
}

// A lease left releasing is finished on a timer, with no call from broker:
// each pass runs the release again, and the worker comes back once the step
// that could not finish can.
func TestAReleasingLeaseIsFinishedOnATimerOnceItCanBe(t *testing.T) {
	r := leasingRig(t)
	rou2 := rou2Of(r)
	if _, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err != nil {
		t.Fatal(err)
	}
	rou2.closeFails(errors.New("rou2 is away"))
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("a release whose rou2 lease did not close answered released")
	}
	state := func() workerpool.State {
		row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1)
		return row.State
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		r.hs.KeepReleasingWorkersEvery(ctx, 5*time.Millisecond)
		close(ended)
	}()
	time.Sleep(40 * time.Millisecond)
	if got := state(); got != workerpool.Releasing {
		t.Errorf("while rou2 is away the row is %s, want releasing", got)
	}
	rou2.closeFails(nil)
	for deadline := time.Now().Add(5 * time.Second); state() != workerpool.Released && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if got := state(); got != workerpool.Released {
		t.Fatalf("with rou2 back the row is %s, want released by the timer", got)
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the timer outlived its context")
	}
	if next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err != nil || next.Box != "worker0-g2" {
		t.Errorf("after the timer's release the key leases %+v (%v), want worker0 at generation 2", next, err)
	}
}
