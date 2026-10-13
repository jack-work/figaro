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

	// With the daemon isolated, softFetchAriaIDs returns nil: what is asserted
	// is that inner was not reached.
	isolateDaemonEnv(t)
	_ = fn(&cmdkit.CompleteContext{Args: []string{"--id"}})
	if called != 0 {
		t.Errorf("inner invoked when cursor after --id (calls=%d)", called)
	}

	got := fn(&cmdkit.CompleteContext{Args: []string{}})
	if called != 1 || len(got) != 1 || got[0] != "INNER" {
		t.Errorf("inner not invoked at first positional; calls=%d got=%v", called, got)
	}

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
	isolateDaemonEnv(t)
	if got := completeAriaIDsPositionalOrFlag(nil); got != nil {
		t.Errorf("expected nil for nil ctx, got %v", got)
	}
	if got := completeAriaIDsPositionalOrFlag(&cmdkit.CompleteContext{Args: []string{"already"}}); got != nil {
		t.Errorf("expected nil mid-position, got %v", got)
	}
}

func TestSoftFetchAriaIDs_NeverPanics(t *testing.T) {
	// With or without a daemon, the call must return well under a second; the
	// value is not asserted.
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
