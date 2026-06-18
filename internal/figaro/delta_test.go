package figaro_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/internal/rpc"
)

// collectMethods drains ch until turn.done, returning the methods seen.
func collectMethods(t *testing.T, ch <-chan rpc.Notification) []string {
	t.Helper()
	var methods []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			methods = append(methods, n.Method)
			if n.Method == rpc.MethodTurnDone {
				return methods
			}
		case <-timeout:
			t.Fatalf("timeout; saw %v", methods)
		}
	}
}

// TestDeltaMode_PerSubscriber proves the open tail is rendered per
// subscriber: a full-mode subscriber gets log.open, a delta-mode one
// gets log.patch — both then get the sealing log.entry and turn.done.
func TestDeltaMode_PerSubscriber(t *testing.T) {
	a := newTestAgent("hello world")
	defer a.Kill()

	full := &chanNotifier{ch: make(chan rpc.Notification, 128)}
	delta := &chanNotifier{ch: make(chan rpc.Notification, 128)}
	a.Subscribe(full)
	a.SubscribeMode(delta, true)

	a.Prompt("hi")

	fullMethods := collectMethods(t, full.ch)
	deltaMethods := collectMethods(t, delta.ch)

	assert.Contains(t, fullMethods, rpc.MethodLogOpen, "full subscriber gets log.open")
	assert.NotContains(t, fullMethods, rpc.MethodLogPatch, "full subscriber gets no patches")

	assert.Contains(t, deltaMethods, rpc.MethodLogPatch, "delta subscriber gets log.patch")
	assert.NotContains(t, deltaMethods, rpc.MethodLogOpen, "delta subscriber gets no full open frames")

	// Both see the seal and the turn end.
	for _, ms := range [][]string{fullMethods, deltaMethods} {
		assert.Contains(t, ms, rpc.MethodLogEntry)
		assert.Contains(t, ms, rpc.MethodTurnDone)
	}
}
