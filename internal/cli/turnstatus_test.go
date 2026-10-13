package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

func TestTurnStatusVocabulary(t *testing.T) {
	for _, tc := range []struct {
		st     turnStatus
		symbol string
		name   string
	}{
		{turnStatusCompleted, "✓", "done"},
		{turnStatusInterrupted, "!", "hup"},
		{turnStatusError, "✗", "error"},
		{turnStatusDisconnected, "⠸", "detached"},
		{turnStatusIdle, "𝄐", "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.st.symbol(0); got != tc.symbol {
				t.Errorf("symbol = %q, want %q", got, tc.symbol)
			}
			if got := tc.st.name(); got != tc.name {
				t.Errorf("name = %q, want %q", got, tc.name)
			}
		})
	}
}

func TestThinkingAnimatesAndHasNoName(t *testing.T) {
	if n := turnStatusThinking.name(); n != "" {
		t.Fatalf("thinking is named %q; it must have no name", n)
	}
	seen := map[string]bool{}
	for tick := uint64(0); tick < 8; tick++ {
		seen[turnStatusThinking.symbol(tick)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("thinking drew %d distinct frames over 8 ticks; it must animate", len(seen))
	}
	for _, st := range []turnStatus{turnStatusCompleted, turnStatusInterrupted, turnStatusError, turnStatusDisconnected, turnStatusIdle} {
		if st.symbol(0) != st.symbol(5) {
			t.Errorf("%s animates; only thinking may", st.name())
		}
	}
}

func TestSuccinctIsTheDefault(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	s.finishTurn("completed")

	succinct := s.turnLabel(false)
	if succinct != "✓" {
		t.Fatalf("succinct label = %q, want the bare glyph", succinct)
	}
	if verbose := s.turnLabel(true); verbose != "done ✓" {
		t.Fatalf("verbose label = %q, want %q", verbose, "done ✓")
	}
}

func TestIdleSaysNothingOnTheBar(t *testing.T) {
	s := newSessionStatus("aria1234", time.Now())
	if l := s.turnLabel(false); l != "" {
		t.Fatalf("a fresh session labelled itself %q", l)
	}
	if l := s.turnLabel(true); l != "" {
		t.Fatalf("verbose idle labelled itself %q", l)
	}
	rows := strings.Join(s.panelLines(), "\n")
	if !strings.Contains(rows, "idle 𝄐") {
		t.Fatalf("the status panel does not name the idle state:\n%s", rows)
	}
}

func TestHupIsNotAnError(t *testing.T) {
	restore := term.SetColorMode(term.ColorAlways)
	defer restore()

	hup := turnStatusInterrupted.paint("hup !")
	bad := turnStatusError.paint("error ✗")
	if hup == bad {
		t.Fatal("hup and error are painted identically; a deliberate stop must not read as a failure")
	}
	if hup == "hup !" {
		t.Fatal("hup is unpainted; it is meant to be gray")
	}
	if !strings.Contains(bad, "error ✗") {
		t.Fatalf("error lost its text: %q", bad)
	}
	if got := turnStatusCompleted.paint("done ✓"); got != "done ✓" {
		t.Fatalf("done is painted %q; it should inherit the row", got)
	}
}

func TestDetachedIsNotIdle(t *testing.T) {
	if turnStatusDisconnected.name() == turnStatusIdle.name() {
		t.Fatal("detached collapsed into idle; the catch-all would then be reported as a known state")
	}
	if turnStatusDisconnected.symbol(0) == turnStatusIdle.symbol(0) {
		t.Fatal("detached and idle draw the same glyph")
	}
}
