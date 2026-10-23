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

	status.beginTurn()
	if line := bar(status, false); !strings.ContainsAny(line, spinnerGlyphs) {
		t.Fatalf("a message on its way is mid-turn, and the row does not say so: %q", line)
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

const spinnerGlyphs = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// Mid-turn is one fact with one picture: a reader must never have to tell a
// "sending" glyph from a "thinking" one, and never see a verdict while the
// aria is still working.
func TestEveryMidTurnStateDrawsTheOneSpinner(t *testing.T) {
	for tick := uint64(0); tick < 32; tick++ {
		want := turnStatusThinking.symbol(tick)
		for _, st := range []turnStatus{turnStatusSending, turnStatusAccepted, turnStatusThinking, turnStatusTooling} {
			got := st.symbol(tick)
			if got != want {
				t.Fatalf("state %d draws %q at tick %d where thinking draws %q", st, got, tick, want)
			}
			if !strings.ContainsAny(got, spinnerGlyphs) {
				t.Fatalf("state %d draws %q, which is not the spinner", st, got)
			}
		}
	}
	if turnStatusThinking.symbol(0) == turnStatusThinking.symbol(1) {
		t.Fatal("the spinner does not animate")
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
