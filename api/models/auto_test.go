package models

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAutoCapacityIncludesEveryEligibleCandidate(t *testing.T) {
	large, small := 1000000, 128000
	auto := &AutoRoute{ContextStatus: "known", Candidates: []RouteCandidate{{Model: "primary", ContextLength: &large}, {Model: "fallback", ContextLength: &small}}}
	entry := Entry{Auto: auto, Context: Context{Default: large, Long: large}}
	require.Equal(t, small, entry.Window(""))
	require.Equal(t, small, entry.Window(LongContextTier))
	cat := catalogOf(t, map[string]Provider{"routed": {IR: IRVersion, Models: map[string]Entry{"auto": entry}}})
	for _, override := range []string{"0", "999999", "64000"} {
		want := small
		if override == "64000" {
			want = 64000
		}
		bound := boundFormOf(map[string]string{"system.provider": `"routed"`, "system.model": `"auto"`, OverrideKey: override})
		require.Equal(t, want, ContextLimit(cat, bound))
	}
	require.Equal(t, small, EntryLimit(cat, "routed", "auto", LongContextTier))
}

func TestUnknownAutoCapacityCannotBorrowPrimaryOrOverride(t *testing.T) {
	known := 1000000
	for _, auto := range []*AutoRoute{
		{ContextStatus: "unknown", Candidates: []RouteCandidate{{ContextLength: &known}}},
		{ContextStatus: "known", Candidates: []RouteCandidate{{ContextLength: &known}, {ContextLength: nil}}},
		{ContextStatus: "known"},
	} {
		entry := Entry{Auto: auto, Context: Context{Default: known, Long: known}}
		require.Zero(t, entry.Window(""))
		cat := catalogOf(t, map[string]Provider{"routed": {IR: IRVersion, Models: map[string]Entry{"auto": entry}}})
		bound := boundFormOf(map[string]string{"system.provider": `"routed"`, "system.model": `"auto"`, OverrideKey: "999999"})
		require.Zero(t, ContextLimit(cat, bound))
		require.Zero(t, EntryLimit(cat, "routed", "auto", LongContextTier))
	}
}
