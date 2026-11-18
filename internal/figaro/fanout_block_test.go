package figaro

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

type stuckNotifier struct {
	entered chan struct{}
	release chan struct{}
}

func (s *stuckNotifier) Notify(string, any) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(string, any) error { return nil }

// A subscriber that stops reading must not hold the agent's lock: a writer
// queued behind it would block every later reader, a queued prompt included.
func TestAStuckSubscriberHoldsNoAgentLock(t *testing.T) {
	a := &Agent{id: "test"}
	stuck := &stuckNotifier{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(stuck.release)
	a.Subscribe(stuck)

	go a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodAriaFrame})
	select {
	case <-stuck.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut never reached the subscriber")
	}

	done := make(chan struct{})
	go func() {
		unsub := a.Subscribe(nopNotifier{})
		unsub()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe waited on a push to a subscriber that stopped reading")
	}
}
