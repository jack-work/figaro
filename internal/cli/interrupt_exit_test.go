package cli

import "testing"

// TestInterruptExit — Ctrl-C on a running turn is a failure, not a success.
//
// WHAT WAS WRONG. plainPrompt (the raw path) has always returned 130, but
// the interactive path's ctx.Done() branch in stream.go interrupted the
// turn, printed "interrupted", and FELL OUT of the select — the process
// exited 0. So `fig send -- … || retry` never fired, and any supervisor
// or `&&` chain treated an abandoned turn as a completed one. Two paths
// through the same gesture, two answers, and the common one was wrong.
func TestInterruptExit(t *testing.T) {
	if got := interruptExit(true); got != 130 {
		t.Errorf("Ctrl-C on a running turn: got %d, want 130 (128+SIGINT)", got)
	}
	if got := interruptExit(false); got != 0 {
		t.Errorf("Ctrl-C with nothing running is a clean close: got %d, want 0", got)
	}
	if exitInterrupted != 130 {
		t.Errorf("exitInterrupted = %d, want 130", exitInterrupted)
	}
}

// TestRawPathUsesTheSameConstant guards the two paths against drifting
// apart again: plain.go's returns and stream.go's exit must be one value.
func TestRawPathUsesTheSameConstant(t *testing.T) {
	if interruptExit(true) != exitInterrupted {
		t.Error("the interactive rule and the raw constant have diverged")
	}
}
