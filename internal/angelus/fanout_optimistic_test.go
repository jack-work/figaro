package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func TestAForkNeverWaitsOnTheHierarchyLock(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 4)
	held := make(chan struct{})
	free := make(chan struct{})
	go func() {
		_ = hs.h.angelus.topology().WithLock(func() error {
			close(held)
			<-free
			return nil
		})
	}()
	<-held
	defer close(free)
	done := make(chan error, 1)
	go func() {
		_, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fork while a topology edit held the lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a fork waited on the hierarchy lock held by a topology edit")
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("top holds %d live children after the fork, want 1", n)
	}
}

func TestConcurrentForksUnderTheLimitAllLand(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	setMaxChildren(t, b, top, 8)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a fork under the limit was refused: %v", err)
	}
	if got := len(drawnChildren(t, hs, top)); got != 8 {
		t.Fatalf("children after 8 concurrent forks = %d, want 8", got)
	}
	requireCacheMatchesTree(t, hs, "8 concurrent forks")
	if _, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a ninth fork past max_children 8 was admitted")
	}
}

func TestAHeldForkSlotCountsUntilReleased(t *testing.T) {
	c := newFanoutCache()
	c.put(fanoutNode{id: "a", parent: "p", live: true})
	pass := func(err error) error { return err }
	h, err := c.take("p", 2, true, 1, false, pass)
	if err != nil {
		t.Fatalf("the second slot of two was refused: %v", err)
	}
	if _, err := c.take("p", 2, true, 1, false, pass); err == nil || !strings.Contains(err.Error(), "holds 2 live children of max_children 2") {
		t.Fatalf("a third hold at max 2: %v, want refused at 2", err)
	}
	if n := c.admitted("p"); n != 2 {
		t.Fatalf("admitted with one child and one held slot = %d, want 2", n)
	}
	c.give(h)
	if n := c.admitted("p"); n != 1 {
		t.Fatalf("admitted after the release = %d, want 1", n)
	}
	if _, err := c.take("p", 2, true, 1, false, pass); err != nil {
		t.Fatalf("a released slot was not free again: %v", err)
	}
}

func TestAForkOfAnAlreadyReapedFigaroIsKept(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	stampReaped(t, b, top, top)
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatalf("fork of a figaro reaped before the fork began: %v", err)
	}
	if _, ok := b.Node(out.Alternative); !ok {
		t.Fatal("the fork of a reaped figaro was discarded")
	}
}

func TestADriftSeenOnceIsNotRebuilt(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	c := hs.h.angelus.fanoutCounts()
	c.put(fanoutNode{id: "in-flight", parent: top, live: true})
	hs.h.angelus.reconcileFanout()
	if n := c.liveChildren(top); n != 1 {
		t.Fatalf("one sweep rebuilt a drift that may be a birth in flight: top holds %d, want 1", n)
	}
	hs.h.angelus.reconcileFanout()
	if n := c.liveChildren(top); n != 0 {
		t.Fatalf("a drift seen in two sweeps was kept: top holds %d, want 0", n)
	}
}

func benchmarkConcurrentForks(b *testing.B, n int) {
	hs, back, outfit := fanoutHandlers(b)
	top, _ := back.CreateConversation(outfit)
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := callFork(b, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
					b.Error(err)
				}
			}()
		}
		wg.Wait()
	}
}

func BenchmarkForks1(b *testing.B) { benchmarkConcurrentForks(b, 1) }
func BenchmarkForks4(b *testing.B) { benchmarkConcurrentForks(b, 4) }
func BenchmarkForks8(b *testing.B) { benchmarkConcurrentForks(b, 8) }

func TestAKillTakesAChildBornAfterItsDeleteSetWasRead(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	out, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	late := out.Alternative
	raw, _ := json.Marshal(top)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.ReapedKey: raw}, nil)
	if _, err := b.ApplyFormPrivileged(top, patch); err != nil {
		t.Fatal(err)
	}
	taken, err := hs.h.reapLateBorn(b, top, []string{top}, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 || taken[0] != late {
		t.Fatalf("late births reaped = %v, want [%s]", taken, late)
	}
	if !hs.h.angelus.isReaped(late) {
		t.Fatal("a child born during its parent's kill stayed live under a reaped parent")
	}
}

type stalledSyncBackend struct {
	*store.XwalBackend
	stall chan struct{}
}

func (s *stalledSyncBackend) SubmitFormPrivileged(id string, patch form.Patch) (func() (uint64, error), error) {
	wait, err := s.XwalBackend.SubmitFormPrivileged(id, patch)
	if err != nil {
		return nil, err
	}
	return func() (uint64, error) {
		<-s.stall
		return wait()
	}, nil
}

func TestAReapWaitingOnItsFormSyncHoldsNoHierarchyLock(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	slow := &stalledSyncBackend{XwalBackend: b, stall: make(chan struct{})}
	hs.h.angelus.Backend = slow
	victim, _ := b.CreateConversation(outfit)
	other, _ := b.CreateConversation(outfit)
	killed := make(chan error, 1)
	go func() {
		raw, _ := json.Marshal(rpc.KillRequest{FigaroID: victim, GraceMS: time.Hour.Milliseconds()})
		_, err := hs.Map[rpc.MethodKill](inProcess(context.Background()), raw)
		killed <- err
	}()
	deadline := time.After(10 * time.Second)
	for !hs.h.angelus.isReaped(victim) {
		select {
		case <-deadline:
			close(slow.stall)
			t.Fatal("the kill never marked its figaro")
		case <-time.After(10 * time.Millisecond):
		}
	}
	locked := make(chan struct{})
	go func() {
		_ = hs.h.angelus.topology().WithLock(func() error { return nil })
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(slow.stall)
		t.Fatal("a reap waiting on its form sync held the hierarchy lock")
	}
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: other}); err != nil {
		t.Fatalf("a fork while a reap waited on its form sync: %v", err)
	}
	select {
	case err := <-killed:
		t.Fatalf("the kill returned before its form sync: %v", err)
	default:
	}
	close(slow.stall)
	if err := <-killed; err != nil {
		t.Fatalf("kill: %v", err)
	}
}

type failingSecondMarkBackend struct {
	*store.XwalBackend
	stall  chan struct{}
	mu     sync.Mutex
	marked []string
}

func (f *failingSecondMarkBackend) SubmitFormPrivileged(id string, patch form.Patch) (func() (uint64, error), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.marked) == 1 {
		return nil, errors.New("the form is closed")
	}
	wait, err := f.XwalBackend.SubmitFormPrivileged(id, patch)
	if err != nil {
		return nil, err
	}
	f.marked = append(f.marked, id)
	return func() (uint64, error) {
		<-f.stall
		return wait()
	}, nil
}

func TestAKillThatFailsMidwayStillAwaitsTheMarksItSubmitted(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	victim, _ := b.CreateConversation(outfit)
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: victim}); err != nil {
		t.Fatal(err)
	}
	f := &failingSecondMarkBackend{XwalBackend: b, stall: make(chan struct{})}
	hs.h.angelus.Backend = f
	killed := make(chan error, 1)
	go func() {
		raw, _ := json.Marshal(rpc.KillRequest{FigaroID: victim, Recursive: true, GraceMS: time.Hour.Milliseconds()})
		_, err := hs.Map[rpc.MethodKill](inProcess(context.Background()), raw)
		killed <- err
	}()
	select {
	case err := <-killed:
		close(f.stall)
		t.Fatalf("the kill returned before the mark it had submitted was durable: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(f.stall)
	if err := <-killed; err == nil {
		t.Fatal("a kill whose second mark failed reported success")
	}
	f.mu.Lock()
	first := f.marked[0]
	f.mu.Unlock()
	if !hs.h.angelus.isReaped(first) {
		t.Fatalf("the mark submitted before the failure (%s) did not land", first)
	}
}
