package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// gen4 bug 5: a 199k aria's new turn opened at "~14.8k/200.0k 7.4%". The
// figures at the start of a turn are an estimate; while the turn warms up the
// bar keeps what it already knew until the estimate passes it.
func TestATurnWarmingUpNeverShowsLessContextThanAlreadyKnown(t *testing.T) {
	s := newSessionStatus("3f8ef5ae", time.Now())
	s.setLimit(200_000)
	s.update(aria.Metrics{ContextTokens: 199_363, ContextExact: true})
	s.setRuntime(runtimeView{State: "thinking", Known: true})

	for _, low := range []int{14_811, 38_382, 114_003} {
		s.update(aria.Metrics{ContextTokens: low, ContextExact: false, TokensOut: 413})
		if line := bar(s, false); !strings.Contains(line, "199.4k/200.0k 99.7%") {
			t.Fatalf("an estimate of %d mid-turn painted below the known 199363: %q", low, line)
		}
	}
	if s.metrics.TokensOut != 413 {
		t.Fatalf("the rest of the figures were dropped with the low estimate: %+v", s.metrics)
	}

	s.update(aria.Metrics{ContextTokens: 199_540, ContextExact: false})
	if line := bar(s, false); !strings.Contains(line, "~199.5k/200.0k 99.8%") {
		t.Fatalf("an estimate above the known value was held back: %q", line)
	}
}

// The floor is the warm-up's only: an exact count, or anything said while the
// aria is idle, is the truth even when it is lower.
func TestALowerContextIsShownWhenExactOrIdle(t *testing.T) {
	s := newSessionStatus("3f8ef5ae", time.Now())
	s.setLimit(200_000)
	s.update(aria.Metrics{ContextTokens: 199_363, ContextExact: true})
	s.setRuntime(runtimeView{State: "thinking", Known: true})
	s.update(aria.Metrics{ContextTokens: 120_000, ContextExact: true})
	if line := bar(s, false); !strings.Contains(line, "120.0k/200.0k 60.0%") {
		t.Fatalf("an exact lower count mid-turn was not shown: %q", line)
	}

	s.finishTurn("stop")
	s.update(aria.Metrics{ContextTokens: 40_000, ContextExact: false})
	if line := bar(s, false); !strings.Contains(line, "~40.0k/200.0k 20.0%") {
		t.Fatalf("a lower estimate on an idle aria was not shown: %q", line)
	}
}
