package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/cmdkit"
)

func TestCompleteAriaIDsAfterFlag_FiresOnlyAfterIDFlag(t *testing.T) {
	called := 0
	inner := func(*cmdkit.CompleteContext) []string {
		called++
		return []string{"INNER"}
	}
	fn := completeAriaIDsAfterFlag(inner)

	// Cursor right after --id: must NOT fall through to inner. The
	// daemon is down in tests so softFetchAriaIDs returns nil — what
	// we're asserting is that inner wasn't reached.
	_ = fn(&cmdkit.CompleteContext{Args: []string{"--id"}})
	if called != 0 {
		t.Errorf("inner invoked when cursor after --id (calls=%d)", called)
	}

	// Cursor at first positional, no --id present: falls through.
	got := fn(&cmdkit.CompleteContext{Args: []string{}})
	if called != 1 || len(got) != 1 || got[0] != "INNER" {
		t.Errorf("inner not invoked at first positional; calls=%d got=%v", called, got)
	}

	// After --id <value> <some key>: also falls through.
	got = fn(&cmdkit.CompleteContext{Args: []string{"--id", "myid"}})
	if called != 2 || len(got) != 1 || got[0] != "INNER" {
		t.Errorf("inner not invoked after --id <value>; calls=%d got=%v", called, got)
	}
}

func TestCompleteAriaIDsAfterFlag_NilInnerSafe(t *testing.T) {
	fn := completeAriaIDsAfterFlag(nil)
	if got := fn(&cmdkit.CompleteContext{Args: []string{"foo"}}); got != nil {
		t.Errorf("expected nil with nil inner, got %v", got)
	}
}

func TestCompleteAriaIDsPositionalOrFlag_HandlesNil(t *testing.T) {
	// Daemon is down in tests; both branches reduce to softFetchAriaIDs
	// which returns nil. We're asserting no panic on nil ctx and the
	// expected nil fall-through for in-the-middle positions.
	if got := completeAriaIDsPositionalOrFlag(nil); got != nil {
		t.Errorf("expected nil for nil ctx, got %v", got)
	}
	// Middle position (already supplied one positional) -> nil.
	if got := completeAriaIDsPositionalOrFlag(&cmdkit.CompleteContext{Args: []string{"already"}}); got != nil {
		t.Errorf("expected nil mid-position, got %v", got)
	}
}

func TestSoftFetchAriaIDs_NeverPanics(t *testing.T) {
	// Whether or not a daemon is running in the test environment,
	// the call must return in well under a second and must not panic.
	// We don't assert the value: a live daemon legitimately returns ids.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = softFetchAriaIDs()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("softFetchAriaIDs exceeded soft-deadline budget")
	}
}
