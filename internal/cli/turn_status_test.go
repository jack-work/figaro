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
		if line := status.statusLineVerbose(100, false, false); !strings.Contains(line, tc.glyph) {
			t.Fatalf("%s: glyph %q missing from the succinct row: %q", tc.reason, tc.glyph, line)
		}
		if line := status.statusLineVerbose(100, false, true); !strings.Contains(line, tc.verbose) {
			t.Fatalf("%s: %q missing from the verbose row: %q", tc.reason, tc.verbose, line)
		}
		// The succinct row carries no words at all.
		if line := status.statusLineVerbose(100, false, false); strings.Contains(line, strings.Fields(tc.verbose)[0]) {
			t.Fatalf("%s: the succinct row leaked the name: %q", tc.reason, line)
		}
	}

	// Thinking animates and is never named, in either mode.
	status.beginTurn()
	if line := status.statusLineVerbose(100, false, true); strings.Contains(line, "thinking") {
		t.Fatalf("thinking is named on the row: %q", line)
	}
	if line := status.statusLineVerbose(100, false, false); !strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("no spinner frame on the row while thinking: %q", line)
	}
}
