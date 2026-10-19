package cli

import (
	"testing"
	"time"
)

// Gluck saw a flash on M-e: the bar showed "…toggle system.methods.enabled
// --id <hex>" for a frame before the answer replaced it. A command that
// answers inside the delay shows only its answer.
func TestAFastCommandShowsNoProgress(t *testing.T) {
	tr := oracleTranscript()
	in := &interactiveInput{mu: new(renderLock), lt: &livelogTurn{tr: tr}}
	notices := func() int {
		tr.status.mu.RLock()
		defer tr.status.mu.RUnlock()
		return len(tr.status.notices)
	}
	in.progressIfSlow("…fast")()
	time.Sleep(2 * progressDelay)
	if n := notices(); n != 0 {
		t.Fatalf("a fast command left %d notices", n)
	}
	finished := in.progressIfSlow("…slow")
	time.Sleep(2 * progressDelay)
	finished()
	if n := notices(); n != 1 {
		t.Fatalf("a slow command left %d notices, want its progress", n)
	}
}
