package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
	"github.com/mattn/go-runewidth"
)

func TestSessionStatusRuleIncludesMantraContextAndTokenCost(t *testing.T) {
	status := newSessionStatus("dac6cb6d", time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC))
	status.update(aria.Metrics{
		Mantra:        "ship a polished app studio",
		ContextTokens: 12000,
		ContextExact:  true,
		TokensIn:      10000,
		TokensOut:     5000,
	})
	status.setLimit(128000)

	rule := status.ruleLine(160, "12-40/97 live")
	for _, want := range []string{"12-40/97 live"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("rule line missing %q: %q", want, rule)
		}
	}
	if got := runewidth.StringWidth(rule); got != 160 {
		t.Fatalf("rule line width = %d, want 160: %q", got, rule)
	}
	line := row(status, 160)
	for _, want := range []string{
		"ship a polished app studio",
		"12.0k/128.0k 9.4%",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q: %q", want, line)
		}
	}
	if got := displayWidth(line); got > 160 {
		t.Fatalf("status line width = %d, want <= 160: %q", got, line)
	}
}

func TestSessionStatusRulePrefersMantraOverSecondaryDetails(t *testing.T) {
	status := newSessionStatus("dac6cb6d", time.Now())
	status.update(aria.Metrics{
		Mantra:        "ship a polished app studio",
		ContextTokens: 12000,
		ContextExact:  true,
		TokensIn:      10000,
		TokensOut:     5000,
	})
	status.setLimit(128000)

	if rule := status.ruleLine(40, ""); strings.Contains(rule, "dac6cb6d") {
		t.Fatalf("the rule still carries the aria id: %q", rule)
	}
	line := row(status, 40)
	if !strings.Contains(line, "dac6cb6d") {
		t.Fatalf("narrow status line dropped the aria: %q", line)
	}
	if !strings.Contains(line, "12.0k/128.0k") {
		t.Fatalf("narrow status line dropped the context: %q", line)
	}
	for _, r := range strings.Split(line, "\n") {
		if displayWidth(r) > 40 {
			t.Fatalf("narrow status row overflows: %q", r)
		}
	}
}
