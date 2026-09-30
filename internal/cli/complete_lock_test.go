package cli

import (
	"sync"
	"testing"
	"time"
)

// THE COMPLETER RUNS WITH in.mu HELD. Keystrokes are dispatched under it
// (stream.go), and Tab calls the completer from dispatch. A completer that
// takes in.mu -- in.currentID() does -- deadlocks the input goroutine against
// itself: the first Tab froze the pager dead, in a real terminal, while every
// fixture test (which supplies its own completer) stayed green.
func TestCompleterDoesNotTakeTheInputLock(t *testing.T) {
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir()) // no daemon: every RPC fails fast
	in := &interactiveInput{figaroID: "aria1234", mu: new(sync.Mutex)}
	in.mu.Lock()
	defer in.mu.Unlock()
	done := make(chan []string, 1)
	go func() { done <- in.complete("attend ") }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the completer blocked while in.mu was held: it must not take the lock its caller holds")
	}
}
