package angelus

import (
	"encoding/json"
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
			if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err != nil {
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
	if _, err := callFork(t, hs, rpc.ForkRequest{FigaroID: top}); err == nil {
		t.Fatal("a ninth fork past max_children 8 was admitted")
	}
}

func TestAHeldForkSlotCountsUntilReleased(t *testing.T) {
	c := newFanoutCache()
	c.put(fanoutNode{id: "a", parent: "p", live: true})
	if _, ok := c.hold("p", 2); !ok {
		t.Fatal("the second slot of two was refused")
	}
	if n, ok := c.hold("p", 2); ok || n != 2 {
		t.Fatalf("a third hold at max 2 = (%d, %t), want refused at 2", n, ok)
	}
	if n := c.admitted("p"); n != 2 {
		t.Fatalf("admitted with one child and one held slot = %d, want 2", n)
	}
	c.release("p")
	if n := c.admitted("p"); n != 1 {
		t.Fatalf("admitted after the release = %d, want 1", n)
	}
	if _, ok := c.hold("p", 2); !ok {
		t.Fatal("a released slot was not free again")
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
