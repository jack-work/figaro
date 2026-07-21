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

	rule := status.ruleLine(160, "12-40/97 live")
	for _, want := range []string{"aria dac6cb6d", "12-40/97 live"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("rule line missing %q: %q", want, rule)
		}
	}
	if got := runewidth.StringWidth(rule); got != 160 {
		t.Fatalf("rule line width = %d, want 160: %q", got, rule)
	}
	line := status.statusLine(160, true)
	for _, want := range []string{
		"ship a polished app studio",
		"ctx 12.0k/128.0k 9.4%",
		"cost 15.0k tok",
		"12:34:56",
		"^/ help",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q: %q", want, line)
		}
	}
	if got := runewidth.StringWidth(line); got > 160 {
		t.Fatalf("status line width = %d, want <= 160: %q", got, line)
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

	rule := status.ruleLine(40, "")
	if !strings.Contains(rule, "aria dac6cb6d") {
		t.Fatalf("narrow rule must retain the id: %q", rule)
	}
	line := status.statusLine(40, true)
	if !strings.Contains(line, "^/ help") {
		t.Fatalf("narrow status line must keep the help hint: %q", line)
	}
	if runewidth.StringWidth(line) > 40 {
		t.Fatalf("narrow status line overflows: %q", line)
	}
}
