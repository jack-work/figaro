package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

func TestTheStatusPanelShowsTheRouteTheBoundFormHolds(t *testing.T) {
	in, _ := capacitySession(t)
	require.NotContains(t, strings.Join(in.lt.status.panelLines(), "\n"), "route")

	bound, _, _ := in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1, Patch: boundFormPatch(bound, map[string]string{
		"system.provider": `"rou2"`, "system.model": `"copilot/anthropic/claude-opus-5"`})})
	require.Contains(t, strings.Join(in.lt.status.panelLines(), "\n"), "  route     rou2 > copilot > anthropic > claude-opus-5")

	bound, _, _ = in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 2, Patch: boundFormPatch(bound, map[string]string{
		"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`})})
	panel := strings.Join(in.lt.status.panelLines(), "\n")
	require.Contains(t, panel, "  route     anthropic > claude-opus-5")
	require.NotContains(t, panel, "rou2")
}

func TestAReplayDropsTheRouteTheLiveSessionLeft(t *testing.T) {
	tapeOnly.Store(true)
	t.Cleanup(func() { tapeOnly.Store(false) })
	in, _ := capacitySession(t)
	in.lt.status.setRoute([]string{"rou2", "explabs", "glm-5.3"})
	in.recordedWindow = 200_000
	in.recomputeLimit()
	require.NotContains(t, strings.Join(in.lt.status.panelLines(), "\n"), "route")
}
