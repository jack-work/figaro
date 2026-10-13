package cli

import (
	"testing"
	"time"
)

// The completer runs with in.mu held (Tab is dispatched under it), so a
// completer that takes in.mu deadlocks the input goroutine.
func TestCompleterDoesNotTakeTheInputLock(t *testing.T) {
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	in := &interactiveInput{figaroID: "aria1234", mu: new(renderLock)}
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
