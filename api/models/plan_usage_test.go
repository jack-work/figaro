package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlanUsageSurvivesTheWireAndTheCatalog(t *testing.T) {
	used := 37.5
	want := &PlanUsage{Plan: "anthropic-max", Windows: []PlanWindow{
		{Window: "five_hour", UsedPercent: &used, ResetAt: time.Date(2026, 11, 19, 19, 20, 0, 0, time.UTC), Status: PlanAllowedWarning, Source: "observed_headers", FetchedAt: time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC), Stale: true},
		{Window: "seven_day"},
	}}
	raw, err := json.Marshal(Entry{PlanUsage: want})
	require.NoError(t, err)
	var back Entry
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, want, back.PlanUsage)
	require.NotContains(t, string(raw), `"used_percent":0`, "an absent percentage is not zero")

	cat := catalogOf(t, map[string]Provider{"rou2": {IR: IRVersion, Node: NewNode(map[string]Entry{
		"anthropic-max/claude-opus-5-5":     {PlanUsage: want},
		"anthropic-max/claude-opus-5-5.1":   {},
		"anthropic-max/claude-opus-5-5-new": {PlanUsage: &PlanUsage{Plan: "anthropic-max"}},
	})}})
	require.Equal(t, want, PlanUsageOf(cat, "rou2", "anthropic-max/claude-opus-5-5"))
	require.Nil(t, PlanUsageOf(cat, "rou2", "anthropic-max/claude-opus-5-5.1"))
	require.Nil(t, PlanUsageOf(cat, "rou2", "anthropic-max/claude-opus-5-5-new"), "no windows is nothing to show")
	require.Nil(t, PlanUsageOf(cat, "rou2", "missing"))
	require.Nil(t, PlanUsageOf(cat, "", "anthropic-max/claude-opus-5-5"))
}
