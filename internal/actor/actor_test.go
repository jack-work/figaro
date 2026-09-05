package actor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/actor"
)

// One implementation, two shapes of user: a handler-driven queue (the form's
// writer) and an owner-drained one (the aria's turn loop).
func TestHandlerDrivenQueueIsFIFO(t *testing.T) {
	var mu sync.Mutex
	var got []int
	done := make(chan struct{})
	q := actor.Start(context.Background(), func(n int) {
		mu.Lock()
		got = append(got, n)
		if len(got) == 3 {
			close(done)
		}
		mu.Unlock()
	}, nil)
	defer q.Close()

	for _, n := range []int{1, 2, 3} {
		if !q.Send(n) {
			t.Fatal("send refused")
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, want := range []int{1, 2, 3} {
		if got[i] != want {
			t.Fatalf("order = %v", got)
		}
	}
}

func TestOwnerDrainedQueueBlocksUntilSent(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, nil)
	defer q.Close()

	recvd := make(chan int, 1)
	go func() {
		n, ok := q.Recv()
		if ok {
			recvd <- n
		}
	}()
	select {
	case n := <-recvd:
		t.Fatalf("Recv returned %d from an empty queue", n)
	case <-time.After(50 * time.Millisecond):
	}
	q.Send(7)
	select {
	case n := <-recvd:
		if n != 7 {
			t.Fatalf("recv = %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not wake")
	}
}

// A closed queue REFUSES rather than dropping silently: a caller waiting on a
// reply must be told, or it waits forever.
func TestCloseRefusesAndUnblocks(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, nil)
	unblocked := make(chan bool, 1)
	go func() {
		_, ok := q.Recv()
		unblocked <- ok
	}()
	q.Close()
	select {
	case ok := <-unblocked:
		if ok {
			t.Fatal("Recv returned an item from a closed empty queue")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Recv")
	}
	if q.Send(1) {
		t.Fatal("a closed queue accepted an item")
	}
	if !q.Closed() {
		t.Fatal("Closed() is false after Close()")
	}
}

// Closed() is read from inside Do in production (an inbox asking whether it may
// still mutate), so it must not take the lock Do holds.
func TestClosedIsSafeInsideDo(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, nil)
	defer q.Close()
	done := make(chan bool, 1)
	go func() {
		q.Do(func(pending []int) []int {
			done <- q.Closed()
			return pending
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Closed() inside Do deadlocked")
	}
}

type headFold struct{}

func (headFold) Coalesce(pending []int) (int, int) {
	if len(pending) < 2 {
		return 0, 0
	}
	return 2, pending[0] + pending[1]
}

func TestCoalescerFoldsTheHead(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, headFold{})
	defer q.Close()
	q.Send(1)
	q.Send(2)
	q.Send(3)
	if n, _ := q.Recv(); n != 3 {
		t.Fatalf("folded = %d, want 3", n)
	}
	if n, _ := q.Recv(); n != 3 {
		t.Fatalf("tail = %d, want 3", n)
	}
}

// TakeWhile never reorders: a prefix cannot jump an item that arrived earlier.
func TestTakeWhileTakesOnlyThePrefix(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, nil)
	defer q.Close()
	for _, n := range []int{2, 4, 5, 6} {
		q.Send(n)
	}
	even := q.TakeWhile(func(n int) bool { return n%2 == 0 })
	if len(even) != 2 {
		t.Fatalf("took %v, want the leading two", even)
	}
	if q.Len() != 2 {
		t.Fatalf("left %d, want 2", q.Len())
	}
}

func TestContextCancelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := actor.Start[int](ctx, nil, nil)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for !q.Closed() {
		if time.Now().After(deadline) {
			t.Fatal("context cancel did not close the queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Close draws the line at ACCEPTANCE, not at the queue: what Send already took
// is still delivered, and only new sends are refused.
//
// The asymmetry is structural. Form.Apply blocks on a reply its handler
// produces, so dropping accepted items would leave every caller past Send
// waiting forever: the hang this is supposed to prevent. The doc used to claim
// the opposite of what the code did, which is worse than either behaviour.
func TestCloseDeliversWhatWasAlreadyAccepted(t *testing.T) {
	q := actor.Start[int](context.Background(), nil, nil)
	for _, n := range []int{1, 2, 3} {
		if !q.Send(n) {
			t.Fatal("send refused before Close")
		}
	}
	q.Close()
	if q.Send(4) {
		t.Fatal("a closed queue accepted a new item")
	}
	for _, want := range []int{1, 2, 3} {
		got, ok := q.Recv()
		if !ok || got != want {
			t.Fatalf("after Close: got (%d, %v), want (%d, true)", got, ok, want)
		}
	}
	if _, ok := q.Recv(); ok {
		t.Fatal("Recv returned a fourth item")
	}
}

// The same rule with a handler draining: an item accepted before Close still
// reaches the handler, so a caller waiting on its reply is answered.
func TestCloseLetsTheHandlerFinishAcceptedWork(t *testing.T) {
	release := make(chan struct{})
	done := make(chan int, 3)
	q := actor.Start(context.Background(), func(n int) {
		if n == 1 {
			<-release
		}
		done <- n
	}, nil)
	for _, n := range []int{1, 2, 3} {
		q.Send(n)
	}
	q.Close()
	close(release)
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 3 accepted items ran after Close", i)
		}
	}
}
