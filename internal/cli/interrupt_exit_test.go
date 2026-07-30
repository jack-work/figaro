package cli

import "testing"

// Ctrl-C on a running turn is a failure, not a success. The raw path always
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
