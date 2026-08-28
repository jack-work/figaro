package cli

import (
	"strings"
	"testing"
	"time"
)

// THE SPELLING CHANGED ON PURPOSE, and this test now pins both halves of the
// split rather than the fused string it used to. Succinct is the default and
// carries the glyph alone; the word is what verbose adds. See
// plans/status-bar-and-modes.md §3.
// bar renders the status row THE WAY THE PROGRAM DOES: through the one
// renderer, so a test cannot pass against a spelling production no longer uses.
func bar(s *sessionStatus, verbose bool) string {
	return strings.Join(s.viewOf(drawerNothing, verbose, time.Now()).render(100), "\n")
}

func TestSessionStatusShowsThinkingAndTerminalOutcomes(t *testing.T) {
	status := newSessionStatus("aria1234", time.Now())
	for _, tc := range []struct {
		reason  string
		glyph   string
		verbose string
	}{
		{"interrupted", "!", "hup !"},
		{"end_turn", "✓", "done ✓"},
		{"error: provider failed", "✗", "error ✗"},
	} {
		status.finishTurn(tc.reason)
		if line := bar(status, false); !strings.Contains(line, tc.glyph) {
			t.Fatalf("%s: glyph %q missing from the succinct row: %q", tc.reason, tc.glyph, line)
		}
		if line := bar(status, true); !strings.Contains(line, tc.verbose) {
			t.Fatalf("%s: %q missing from the verbose row: %q", tc.reason, tc.verbose, line)
		}
		// The succinct row carries no words at all.
		if line := bar(status, false); strings.Contains(line, strings.Fields(tc.verbose)[0]) {
			t.Fatalf("%s: the succinct row leaked the name: %q", tc.reason, line)
		}
	}

	// Thinking animates and is never named, in either mode.
	status.beginTurn()
	if line := bar(status, true); strings.Contains(line, "thinking") {
		t.Fatalf("thinking is named on the row: %q", line)
	}
	if line := bar(status, false); !strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("no spinner frame on the row while thinking: %q", line)
	}
}
