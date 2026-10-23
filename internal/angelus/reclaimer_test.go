package angelus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The outage this defends against: a removal that hangs (a Detach copy, a
// topology wait, a dead disk) used to run on the sweep goroutine and stall
// dead-pid reaping, cache budgets and hibernation behind it.
func TestASlowRemovalNeverStallsTheSweep(t *testing.T) {
	a, _, _ := ttlSweepFixture(t, "slow", "also")
	release := make(chan struct{})
	var calls atomic.Int32
	a.RemoveAria = func(string, bool) error {
		calls.Add(1)
		<-release
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.reclaims().run(ctx)

	for i := 0; i < 5; i++ {
		start := time.Now()
		a.expireTTL()
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("sweep %d took %v while a removal hangs; it must only enqueue", i, d)
		}
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d removals started while the first hangs; one at a time, no duplicates", n)
	}
	close(release)
}

func TestAFailingRemovalBacksOff(t *testing.T) {
	a, _, _ := ttlSweepFixture(t, "broken")
	var calls int
	a.RemoveAria = func(string, bool) error {
		calls++
		return errors.New("disk on fire")
	}
	r := a.reclaims()
	clock := time.Now()
	r.now = func() time.Time { return clock }

	for i := 0; i < 10; i++ {
		sweepNow(a)
	}
	if calls != 1 {
		t.Fatalf("%d attempts in ten sweeps; a failure must back off, not retry every sweep", calls)
	}
	clock = clock.Add(reclaimBackoffMin + time.Second)
	sweepNow(a)
	if calls != 2 {
		t.Fatalf("%d attempts after the backoff; it must retry once the window passes", calls)
	}
	sweepNow(a)
	if calls != 2 {
		t.Fatal("the second failure did not lengthen the backoff")
	}
	if f := r.failing()["broken"]; f.attempts != 2 || f.next.Sub(clock) != 2*reclaimBackoffMin {
		t.Fatalf("failure record %+v, want 2 attempts and a doubled window", f)
	}
	clock = clock.Add(100 * reclaimBackoffMax)
	for i := 0; i < 10; i++ {
		clock = clock.Add(reclaimBackoffMax + time.Second)
		sweepNow(a)
	}
	if f := r.failing()["broken"]; f.next.Sub(clock) > reclaimBackoffMax {
		t.Fatalf("backoff grew to %v; it is capped at %v", f.next.Sub(clock), reclaimBackoffMax)
	}
}

func TestNothingIsRemovedWhileTheRoleIndexWarms(t *testing.T) {
	a, be, removed := ttlSweepFixture(t, "due")
	be.rolesWarming = true
	sweepNow(a)
	if len(*removed) != 0 {
		t.Fatalf("removed %v before the role index was read; a role may point at it", *removed)
	}
	if _, failing := a.reclaims().failing()["due"]; failing {
		t.Fatal("waiting for the index was counted as a failure")
	}
	be.rolesWarming = false
	sweepNow(a)
	if len(*removed) != 1 {
		t.Fatalf("removed %v once the index was ready, want [due]", *removed)
	}
}
