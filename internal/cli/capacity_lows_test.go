package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
)

func statusPitText(in *interactiveInput) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	var rows []string
	for _, r := range in.lt.tr.pit.list().rows {
		rows = append(rows, r.text)
	}
	return strings.Join(rows, "\n")
}

// A replay draws only what the tape recorded, so plan usage the live session
// left on the bar goes with the live limit.
func TestAReplayClearsThePlanUsageTheLiveSessionLeft(t *testing.T) {
	tapeOnly.Store(true)
	t.Cleanup(func() { tapeOnly.Store(false) })
	in := capacitySession(t)
	in.lt.status.setPlanUsage(&models.PlanUsage{Windows: []models.PlanWindow{{Window: "five_hour", UsedPercent: pct(37)}}})
	in.recordedWindow = 200_000
	in.recomputeLimit()
	require.NotContains(t, strings.Join(in.lt.status.panelLines(), "\n"), "5h 37%")
}

// The status pit is a snapshot of the panel; one left open must follow the
// limit and the plan when they move.
func TestAnOpenStatusPitFollowsTheLimit(t *testing.T) {
	tapeOnly.Store(true)
	t.Cleanup(func() { tapeOnly.Store(false) })
	in := capacitySession(t)
	in.mu.Lock()
	in.lt.tr.openStatusPit()
	in.mu.Unlock()
	require.NotContains(t, statusPitText(in), "200.0k")
	in.recordedWindow = 200_000
	in.recomputeLimit()
	require.Contains(t, statusPitText(in), "200.0k")
}
