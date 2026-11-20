package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathIsTheIDSplitOnSlashes(t *testing.T) {
	require.Equal(t, []string{"claude-opus-5"}, Path("claude-opus-5"))
	require.Equal(t, []string{"explabs", "glm-5.3"}, Path("explabs/glm-5.3"))
	require.Equal(t, []string{"copilot", "anthropic", "claude-opus-5"}, Path("copilot/anthropic/claude-opus-5"))
	for _, odd := range []string{"/x", "x/", "a//b", "/"} {
		require.Equal(t, []string{odd}, Path(odd), "an empty segment is no upstream: %q", odd)
	}
}

func TestRouteRunsFromTheProviderDownToTheModel(t *testing.T) {
	require.Equal(t, []string{"anthropic", "claude-opus-5"}, Route("anthropic", "claude-opus-5"))
	require.Equal(t, []string{"rou2", "explabs", "glm-5.3"}, Route("rou2", "explabs/glm-5.3"))
	require.Equal(t, []string{"rou2", "copilot", "anthropic", "claude-opus-5"}, Route("rou2", "copilot/anthropic/claude-opus-5"))
	require.Equal(t, []string{"rou2"}, Route("rou2", ""))
	require.Nil(t, Route("", "claude-opus-5"))
}

func TestEntryKeyWalksOneBranchPerUpstream(t *testing.T) {
	require.Equal(t, "anthropic.models.claude-opus-5", EntryKey("anthropic", "claude-opus-5"))
	require.Equal(t, "rou2.upstreams.explabs.models.glm-5.3", EntryKey("rou2", "explabs/glm-5.3"))
	require.Equal(t, "rou2.upstreams.copilot.upstreams.anthropic.models.claude-opus-5", EntryKey("rou2", "copilot/anthropic/claude-opus-5"))
}

func TestANodeKeepsAnyDepthAndFlattensBackToIDs(t *testing.T) {
	flat := map[string]Entry{
		"claude-opus-5":                   {Name: "direct"},
		"explabs/glm-5.3":                 {Name: "glm"},
		"copilot/anthropic/claude-opus-5": {Name: "deep"},
		"copilot/gpt-6":                   {Name: "shallow"},
		"/odd":                            {Name: "odd"},
	}
	n := NewNode(flat)
	require.Equal(t, flat, n.Flat())
	require.Contains(t, n.Leaves, "claude-opus-5")
	require.Contains(t, n.Branches["copilot"].Branches["anthropic"].Leaves, "claude-opus-5")
	for id, want := range flat {
		got, ok := n.Find(id)
		require.True(t, ok, id)
		require.Equal(t, want, got, id)
	}
	_, ok := n.Find("copilot/anthropic/missing")
	require.False(t, ok)
	_, ok = n.Find("nowhere/claude-opus-5")
	require.False(t, ok)
}

func TestTheSameModelSitsUnderEveryParentThatReachesIt(t *testing.T) {
	both := catalogOf(t, map[string]Provider{
		"anthropic": {IR: IRVersion, Node: NewNode(map[string]Entry{"claude-opus-5": {Context: Context{Default: 200_000}}})},
		"rou2": {IR: IRVersion, Node: NewNode(map[string]Entry{
			"anthropic-max/claude-opus-5": {Context: Context{Default: 1_000_000}},
			"anthropic-kic/claude-opus-5": {Context: Context{Default: 500_000}},
		})},
	})
	require.Equal(t, 200_000, EntryLimit(both, "anthropic", "claude-opus-5", ""))
	require.Equal(t, 1_000_000, EntryLimit(both, "rou2", "anthropic-max/claude-opus-5", ""))
	require.Equal(t, 500_000, EntryLimit(both, "rou2", "anthropic-kic/claude-opus-5", ""))
	require.Zero(t, EntryLimit(both, "rou2", "claude-opus-5", ""), "a bare id is not under rou2")
}

func TestLookupAndPlanUsageReachADeepLeaf(t *testing.T) {
	used := 41.0
	cat := catalogOf(t, map[string]Provider{
		"rou2": {IR: IRVersion, Node: NewNode(map[string]Entry{
			"copilot/anthropic/claude-opus-5": {
				Context:   Context{Default: 128_000},
				PlanUsage: &PlanUsage{Windows: []PlanWindow{{Window: "five_hour", UsedPercent: &used}}},
			},
		})},
	})
	e, ok := Lookup(cat, "rou2", "copilot/anthropic/claude-opus-5")
	require.True(t, ok)
	require.Equal(t, 128_000, e.Context.Default)
	u := PlanUsageOf(cat, "rou2", "copilot/anthropic/claude-opus-5")
	require.NotNil(t, u)
	require.Equal(t, 41.0, *u.Windows[0].UsedPercent)
}
