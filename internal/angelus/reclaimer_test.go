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

// A batch removes back to back; one delete that is refused (its delete set
// changed, a role appeared, the disk failed) must not hold up the others in
// the same pass, and must back off on its own.
func TestABatchWithOneRefusedDeleteRemovesTheRest(t *testing.T) {
	var done []string
	r := newReclaimer(func(id string) error {
		done = append(done, id)
		if id == "refused" {
			return errors.New("delete set changed")
		}
		return nil
	})
	r.batch = 4
	for _, id := range []string{"a", "refused", "c", "d"} {
		r.enqueue(id)
	}
	if !r.pass(context.Background()) {
		t.Fatal("a pass with work reported nothing done")
	}
	if len(done) != 4 {
		t.Fatalf("attempted %v in one pass, want all four", done)
	}
	if !r.quiet() {
		t.Fatal("the batch left work queued or in flight")
	}
	fails := r.failing()
	if _, ok := fails["refused"]; !ok || len(fails) != 1 {
		t.Fatalf("failures %v, want only the refused delete backing off", fails)
	}
}

func TestAPassTakesAtMostOneBatch(t *testing.T) {
	var done int
	r := newReclaimer(func(string) error { done++; return nil })
	r.batch = 2
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		r.enqueue(id)
	}
	r.pass(context.Background())
	if done != 2 {
		t.Fatalf("one pass removed %d, want the batch of 2", done)
	}
	r.pass(context.Background())
	r.pass(context.Background())
	if done != 5 || !r.quiet() {
		t.Fatalf("three passes removed %d of 5", done)
	}
	if r.pass(context.Background()) {
		t.Fatal("an empty queue reported a pass with work")
	}
}

// El Inspector's condition for batching: every member of a batch is judged on
// its own at commit time. A role that comes to point at one member, or a fresh
// write to another, between the sweep's decision and the batch's commit skips
// that member only; the rest of the batch is removed as decided.
func TestABatchJudgesEachMemberAtCommit(t *testing.T) {
	a, be, removed := ttlSweepFixture(t, "plain", "targeted", "written", "also")
	for _, id := range []string{"plain", "targeted", "written", "also"} {
		be.lastTS[id] = hoursAgo(25)
	}
	r := a.reclaims()
	r.batch = 8
	a.expireTTL()

	be.roles["@role"] = "targeted"
	be.lastTS["written"] = hoursAgo(0)

	r.pass(context.Background())
	got := map[string]bool{}
	for _, id := range *removed {
		got[id] = true
	}
	if !got["plain"] || !got["also"] || len(got) != 2 {
		t.Fatalf("removed %v, want exactly [plain also]: the targeted and freshly written members are skipped, the rest go", *removed)
	}
	if len(r.failing()) != 0 {
		t.Fatalf("skips were counted as failures: %v", r.failing())
	}
}
