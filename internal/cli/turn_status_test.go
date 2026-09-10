package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

// THE SPELLING CHANGED ON PURPOSE, and this test now pins both halves of the
// split rather than the fused string it used to. Succinct is the default and
// carries the glyph alone; the word is what verbose adds. See
// plans/status-bar-and-modes.md §3.
// bar renders the status row THE WAY THE PROGRAM DOES: through the one
// renderer, so a test cannot pass against a spelling production no longer uses.
func bar(s *sessionStatus, verbose bool) string {
	return strings.Join(s.viewOf(pitNothing, verbose, time.Now()).render(100), "\n")
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

	// A SUBMIT IS "SENDING", NOT "THINKING", and this assertion changed on
	// purpose. beginTurn used to claim the model was working the instant a
	// prompt was accepted -- before it had round-tripped, before the first
	// token, and possibly while it sat in a queue behind another turn. The
	// client may only assert facts about ITSELF; everything past the socket
	// arrives from the runtime intrinsic form.
	status.beginTurn()
	if line := bar(status, false); !strings.ContainsAny(line, string(sendingFrames)) {
		t.Fatalf("no departure frame on the row while sending: %q", line)
	}
	if line := bar(status, false); strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("the bar claimed the model was THINKING about a message that has "+
			"not been acknowledged by anything: %q", line)
	}

	// Thinking animates and is never named, in either mode -- and it is now
	// reached only by the daemon SAYING so.
	if !status.setRuntime(runtimeView{State: "thinking", Known: true}) {
		t.Fatal("a runtime patch moving the bar to thinking reported no change")
	}
	if line := bar(status, true); strings.Contains(line, "thinking") {
		t.Fatalf("thinking is named on the row: %q", line)
	}
	if line := bar(status, false); !strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("no spinner frame on the row while thinking: %q", line)
	}
}

// THE THREE MOVING STATES MUST BE TELLABLE APART. A reader distinguishes
// FAMILIES of motion at a glance and speeds never, so this asserts the glyph
// sets are disjoint -- which is the property, rather than asserting the
// particular glyphs, which are a taste.
func TestMovingStatesUseDistinctGlyphFamilies(t *testing.T) {
	families := map[turnStatus]map[string]bool{}
	for _, st := range []turnStatus{turnStatusSending, turnStatusAccepted, turnStatusThinking} {
		set := map[string]bool{}
		for tick := uint64(0); tick < 32; tick++ {
			set[st.symbol(tick)] = true
		}
		if len(set) < 2 {
			t.Fatalf("state %d does not animate: it drew %d distinct glyphs over 32 ticks, "+
				"which is a still picture of a thing that is moving", st, len(set))
		}
		families[st] = set
	}
	for a, sa := range families {
		for b, sb := range families {
			if a >= b {
				continue
			}
			for g := range sa {
				if sb[g] {
					t.Fatalf("states %d and %d share the glyph %q. Two moving indicators that "+
						"overlap are one indicator as far as a reader is concerned", a, b, g)
				}
			}
		}
	}
}

// Ctrl-C must interrupt for the WHOLE time a message is in flight, including
// before any provider round has started. Otherwise the window between submit
// and the first token is a window where the user's stop key silently means
// something else.
func TestSendingAndAcceptedCountAsTurnRunning(t *testing.T) {
	for _, st := range []turnStatus{turnStatusSending, turnStatusAccepted, turnStatusThinking, turnStatusTooling} {
		status := newSessionStatus("aria1234", time.Now())
		status.setTurn(st)
		if !status.turnRunning() {
			t.Fatalf("state %d does not count as a turn in flight, so Ctrl-C there would "+
				"exit cleanly instead of interrupting", st)
		}
		if !status.advance() {
			t.Fatalf("state %d does not animate, so its indicator is a still picture", st)
		}
	}
	status := newSessionStatus("aria1234", time.Now())
	status.finishTurn("end_turn")
	if status.turnRunning() {
		t.Fatal("a completed turn reports itself as still running")
	}
	if status.advance() {
		t.Fatal("a completed turn is animating")
	}
}

// A GUESS MAY NOT OVERWRITE A FACT.
//
// beginTurn runs from openInline, which happens AFTER the submit -- so on a
// fast aria the daemon's "thinking" lands first. Setting sending there
// unconditionally put the bar back on the departure arrows with a tool
// visibly running above it, measured in a pty.
func TestABeginTurnDoesNotClobberAnAuthoritativeState(t *testing.T) {
	for _, st := range []rpc.RuntimeState{rpc.RuntimeAccepted, rpc.RuntimeThinking, rpc.RuntimeTooling} {
		status := newSessionStatus("aria1234", time.Now())
		status.setRuntime(runtimeView{State: st, Known: true})
		before := status.turnLabelForTest()
		status.beginTurn()
		if after := status.turnLabelForTest(); after != before {
			t.Fatalf("the daemon said %q and a late beginTurn moved the bar from %v to %v. "+
				"Only `sending` is the client's own hypothesis; everything else is a "+
				"fact and outranks it", st, before, after)
		}
	}

	// But from a state the client owns, it still arms.
	status := newSessionStatus("aria1234", time.Now())
	status.finishTurn("end_turn")
	status.beginTurn()
	if status.turnLabelForTest() != turnStatusSending {
		t.Fatal("a fresh submit did not arm the sending indicator, so the footer would " +
			"say nothing between the keystroke and the daemon's first word")
	}
}
