package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

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
		if line := bar(status, false); strings.Contains(line, strings.Fields(tc.verbose)[0]) {
			t.Fatalf("%s: the succinct row leaked the name: %q", tc.reason, line)
		}
	}

	// A submit is "sending": the client asserts only facts about itself;
	// thinking comes from the runtime form.
	status.beginTurn()
	if line := bar(status, false); !strings.ContainsAny(line, string(sendingFrames)) {
		t.Fatalf("no departure frame on the row while sending: %q", line)
	}
	if line := bar(status, false); strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("the bar claimed the model was THINKING about a message that has "+
			"not been acknowledged by anything: %q", line)
	}

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

// The glyph sets of the moving states are disjoint; the glyphs themselves are a taste.
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

// Ctrl-C must interrupt for the whole time a message is in flight, before the
// first token included.
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

// beginTurn runs after the submit, so the daemon's state may already have landed
// and outranks it.
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

	status := newSessionStatus("aria1234", time.Now())
	status.finishTurn("end_turn")
	status.beginTurn()
	if status.turnLabelForTest() != turnStatusSending {
		t.Fatal("a fresh submit did not arm the sending indicator, so the footer would " +
			"say nothing between the keystroke and the daemon's first word")
	}
}

func TestThePagerStartsWithTheInquiryHeaderOn(t *testing.T) {
	set := pagerSettings(nil)
	if !set.sticky {
		t.Fatal("a pager session starts with the inquiry header OFF, so a reader inside " +
			"a long answer must press `s` every session to learn which question they " +
			"are inside")
	}
	if listen := listenSettings(nil); !listen.sticky || !listen.listen {
		t.Fatalf("listen settings lost a field: sticky=%v listen=%v", listen.sticky, listen.listen)
	}
}
