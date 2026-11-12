package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func retirementSnap(t *testing.T, v any) form.Snapshot {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return form.FromMap(map[string]json.RawMessage{SystemRetirementKey: raw})
}

func TestRetirementThresholds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  any
		window int
		want   int
		ttl    time.Duration
	}{
		{"tokens", map[string]any{"context_threshold": 700000, "ttl": "72h"}, 0, 700000, 72 * time.Hour},
		{"tokens as a string", map[string]any{"context_threshold": "700000"}, 1_000_000, 700000, 0},
		{"percent of the window", map[string]any{"context_threshold": "60%"}, 200_000, 120_000, 0},
		{"percent with no known window", map[string]any{"context_threshold": "60%"}, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RetirementOf(retirementSnap(t, tc.value))
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Threshold(tc.window); got != tc.want {
				t.Fatalf("threshold %d, want %d", got, tc.want)
			}
			if r.TTL != tc.ttl {
				t.Fatalf("ttl %v, want %v", r.TTL, tc.ttl)
			}
		})
	}
}

func TestRetirementRefusesNonsense(t *testing.T) {
	for _, v := range []any{"0", -5, "150%", "0%", "lots", map[string]any{"a": 1}} {
		if _, err := RetirementOf(retirementSnap(t, map[string]any{"context_threshold": v})); err == nil {
			t.Errorf("context_threshold %v accepted", v)
		}
	}
	for name, v := range map[string]any{
		"bad ttl":      map[string]any{"context_threshold": 10, "ttl": "soon"},
		"negative ttl": map[string]any{"context_threshold": 10, "ttl": "-1h"},
		"typo":         map[string]any{"context_treshold": 10},
		"ttl alone":    map[string]any{"ttl": "1h"},
		"not a table":  "60%",
		"empty table":  map[string]any{},
	} {
		if _, err := RetirementOf(retirementSnap(t, v)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

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

func TestRetirementActionDefaultsToRetireAndSucceedCarriesItsFields(t *testing.T) {
	r, err := ParseRetirement(json.RawMessage(`{"context_threshold": 1000}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != RetirementRetire || r.Succeeds() {
		t.Fatalf("action = %q: a table without one must behave as before", r.Action)
	}
	if r.SuccessionTimeout() != DefaultSuccessionTimeout || r.QuotedTurns() != DefaultRampTurns {
		t.Fatalf("defaults: timeout %s, turns %d", r.SuccessionTimeout(), r.QuotedTurns())
	}

	r, err = ParseRetirement(json.RawMessage(`{"context_threshold": 1000, "action": "succeed", "page": "@king", "timeout": "10m", "ramp": "/etc/ramp.md", "ramp_turns": 7}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Succeeds() || r.Page != "@king" || r.SuccessionTimeout() != 10*time.Minute || r.Ramp != "/etc/ramp.md" || r.QuotedTurns() != 7 {
		t.Fatalf("parsed %+v", r)
	}
	if r.WarnAt(0) != 800 {
		t.Fatalf("warn at %d, want 80%% of the threshold", r.WarnAt(0))
	}
}

func TestMalformedSuccessionFieldsAreRefusedByName(t *testing.T) {
	for field, v := range map[string]string{
		"action":     `"abdicate"`,
		"page":       `""`,
		"timeout":    `"0"`,
		"ramp":       `""`,
		"ramp_turns": `0`,
	} {
		_, err := ParseRetirement(json.RawMessage(`{"context_threshold": 1000, "` + field + `": ` + v + `}`))
		if err == nil || !strings.Contains(err.Error(), SystemRetirementKey+"."+field) {
			t.Errorf("%s = %s: %v", field, v, err)
		}
	}
}
