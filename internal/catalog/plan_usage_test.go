package catalog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestARou2RowCarriesItsPlanUsage(t *testing.T) {
	got := read(t, Mapping{List: "data"}, nil, `{"data":[
		{"id":"anthropic-max/claude-opus-5-5","plan_usage":{"plan":"anthropic-max","windows":[
			{"window":"five_hour","used_percent":37.5,"reset_at":"2026-11-19T19:20:00Z","status":"allowed","source":"provider_reported","fetched_at":"2026-11-19T18:00:00Z","stale":false}]}},
		{"id":"anthropic-max/empty","plan_usage":{"plan":"anthropic-max","windows":[]}},
		{"id":"anthropic-max/garbled","plan_usage":"soon"},
		{"id":"explabs/glm-5.3"}
	]}`)
	u := got["anthropic-max/claude-opus-5-5"].PlanUsage
	require.NotNil(t, u)
	require.Equal(t, "anthropic-max", u.Plan)
	require.Len(t, u.Windows, 1)
	w := u.Windows[0]
	require.Equal(t, "five_hour", w.Window)
	require.NotNil(t, w.UsedPercent)
	require.Equal(t, 37.5, *w.UsedPercent)
	require.True(t, w.ResetAt.Equal(time.Date(2026, 11, 19, 19, 20, 0, 0, time.UTC)))
	require.Equal(t, "allowed", w.Status)
	require.Equal(t, "provider_reported", w.Source)
	for _, id := range []string{"anthropic-max/empty", "anthropic-max/garbled", "explabs/glm-5.3"} {
		require.Nil(t, got[id].PlanUsage, id)
	}
}

func TestNullPlanFieldsReadAsUnknown(t *testing.T) {
	got := read(t, Mapping{List: "data"}, nil, `{"data":[
		{"id":"m","plan_usage":{"plan":"anthropic-max","plan_label":null,"windows":[
			{"window":"five_hour","used_percent":null,"reset_at":null,"status":null,"source":"observed_headers","fetched_at":"2026-11-19T18:00:00Z","stale":true}]}}
	]}`)
	u := got["m"].PlanUsage
	require.NotNil(t, u)
	w := u.Windows[0]
	require.Nil(t, w.UsedPercent)
	require.True(t, w.ResetAt.IsZero())
	require.Empty(t, w.Status)
	require.True(t, w.Stale)
}
