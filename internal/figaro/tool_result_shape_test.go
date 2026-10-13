package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// compose matches results to invokes by id and treats a second result as
// closing nothing, so this package must emit exactly one result per call in one tic.
func TestEveryCallGetsExactlyOneResultInOneTic(t *testing.T) {
	calls := []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "tc_ok", ToolName: "bash", Arguments: map[string]any{"command": "true"}},
		{Type: message.ContentToolInvoke, ToolCallID: "tc_unexpected", ToolName: "bash", Arguments: map[string]any{"command": "true"}},
		{Type: message.ContentToolInvoke, ToolCallID: "tc_never_ran", ToolName: "bash", Arguments: map[string]any{"command": "true"}},
	}
	expect := map[string]bool{"tc_ok": true, "tc_never_ran": true}
	outcomes := map[string]toolOutcome{"tc_ok": {
		content: []message.Content{{Type: message.ContentProse, Text: "ok"}},
	}}

	// isInterrupted() is false on a zero Agent; both paths are checked.
	for _, interrupted := range []bool{false, true} {
		a := &Agent{}
		a.mu.Lock()
		a.interrupted = interrupted
		a.mu.Unlock()
		tic := a.assembleToolResults(calls, expect, outcomes)
		checkOneResultPerCall(t, interrupted, calls, tic)
	}
}

func checkOneResultPerCall(t *testing.T, interrupted bool, calls []message.Content, tic message.Message) {
	t.Helper()

	byID := map[string]int{}
	for _, c := range tic.Content {
		if c.Type != message.ContentToolResult {
			continue
		}
		byID[c.ToolCallID]++
	}
	if len(byID) != len(calls) {
		t.Fatalf("interrupted=%v: one tic carried results for %d of %d calls: %v", interrupted, len(byID), len(calls), byID)
	}
	for id, n := range byID {
		if n != 1 {
			t.Errorf("call %s got %d result blocks in one tic; the composer's boundary treats a second result as closing nothing,\n"+
				"so a duplicate would leave a streaming sibling inside the memoized prefix with frozen output", id, n)
		}
	}
}
