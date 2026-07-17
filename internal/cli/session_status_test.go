package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/mattn/go-runewidth"
)

func TestSessionStatusRuleIncludesMantraContextAndTokenCost(t *testing.T) {
	status := newSessionStatus("dac6cb6d", time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC))
	status.update(aria.Metrics{
		Mantra:        "ship a polished app studio",
		ContextTokens: 12000,
		ContextLimit:  128000,
		ContextExact:  true,
		TokensIn:      10000,
		TokensOut:     5000,
	})

	line := sessionStatusRule(status, 160, "")
	for _, want := range []string{
		"dac6cb6d",
		"ship a polished app studio",
		"ctx 12.0k/128.0k 9.4%",
		"cost 15.0k tok",
		"12:34:56",
		"? help",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q: %q", want, line)
		}
	}
	if got := runewidth.StringWidth(line); got != 160 {
		t.Fatalf("status line width = %d, want 160: %q", got, line)
	}
}

func TestSessionStatusRulePrefersMantraOverSecondaryDetails(t *testing.T) {
	status := newSessionStatus("dac6cb6d", time.Now())
	status.update(aria.Metrics{
		Mantra:        "ship a polished app studio",
		ContextTokens: 12000,
		ContextLimit:  128000,
		ContextExact:  true,
		TokensIn:      10000,
		TokensOut:     5000,
	})

	line := sessionStatusRule(status, 64, "")
	if !strings.Contains(line, "dac6cb6d") || !strings.Contains(line, "ship a polished") {
		t.Fatalf("narrow status must retain id and mantra: %q", line)
	}
}
