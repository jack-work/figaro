package cli

import (
	"strings"
	"testing"
	"time"
)

func TestSessionStatusShowsThinkingAndTerminalOutcomes(t *testing.T) {
	status := newSessionStatus("aria1234", time.Now())
	status.beginTurn()
	if line := sessionStatusRule(status, 100, ""); !strings.Contains(line, "thinking") {
		t.Fatalf("thinking state missing: %q", line)
	}
	status.finishTurn("interrupted")
	if line := sessionStatusRule(status, 100, ""); !strings.Contains(line, "interrupted !") {
		t.Fatalf("interrupted state missing: %q", line)
	}
	status.finishTurn("end_turn")
	if line := sessionStatusRule(status, 100, ""); !strings.Contains(line, "completed ✓") {
		t.Fatalf("completed state missing: %q", line)
	}
	status.finishTurn("error: provider failed")
	if line := sessionStatusRule(status, 100, ""); !strings.Contains(line, "error ✗") {
		t.Fatalf("error state missing: %q", line)
	}
}
