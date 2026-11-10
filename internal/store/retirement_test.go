package store

import "testing"

func TestRetiredReasonReadsOldStampsAsManual(t *testing.T) {
	for raw, want := range map[string]string{
		"context":              RetiredContext,
		"manual":               RetiredManual,
		"2026-10-21T06:00:00Z": RetiredManual,
		"true":                 RetiredManual,
		"false":                "",
		"":                     "",
	} {
		if got := RetiredReason(raw); got != want {
			t.Errorf("RetiredReason(%q) = %q, want %q", raw, got, want)
		}
	}
}
