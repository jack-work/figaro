package cli

import (
	"strings"
	"testing"
	"time"
)

func TestSessionStatusShowsThinkingAndTerminalOutcomes(t *testing.T) {
	status := newSessionStatus("aria1234", time.Now())
	status.beginTurn()
	if line := status.statusLine(100, false); !strings.Contains(line, "thinking") {
		t.Fatalf("thinking state missing: %q", line)
	}
	status.finishTurn("interrupted")
	if line := status.statusLine(100, false); !strings.Contains(line, "interrupted !") {
		t.Fatalf("interrupted state missing: %q", line)
	}
	status.finishTurn("end_turn")
	if line := status.statusLine(100, false); !strings.Contains(line, "completed ✓") {
		t.Fatalf("completed state missing: %q", line)
	}
	status.finishTurn("error: provider failed")
	if line := status.statusLine(100, false); !strings.Contains(line, "error ✗") {
		t.Fatalf("error state missing: %q", line)
	}
}
