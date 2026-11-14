package figaro_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAutoAgentMetricsUseAllCandidatesAndKeepUnknownExplicit(t *testing.T) {
	large, small := 1000000, 128000
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider":           json.RawMessage(`"metrics"`),
		"system.model":              json.RawMessage(`"auto"`),
		"system.max_context_tokens": json.RawMessage(`999999`),
	}, nil))
	cat := &fakeCatalog{}
	backend, id := store.NewTestAria(t, "auto-context", message.Patch{})
	a := figaro.NewAgent(figaro.Config{Backend: backend, Projector: uiir.New(nil), ID: id, Provider: metricsProvider{}, Form: cb, Models: cat})
	defer a.Kill()
	auto := &models.AutoRoute{ContextStatus: "known", Candidates: []models.RouteCandidate{{ContextLength: &large}, {ContextLength: &small}}}
	fill := func() {
		raw, err := json.Marshal(models.Provider{IR: models.IRVersion, Models: map[string]models.Entry{"auto": {Auto: auto, Context: models.Context{Default: large}}}})
		require.NoError(t, err)
		cat.mu.Lock()
		cat.snap = cat.snap.Apply(form.Build(cat.snap, map[string]json.RawMessage{"metrics": raw}, nil))
		cat.mu.Unlock()
	}
	fill()
	require.Equal(t, small, a.Info().ContextLimit)
	auto.ContextStatus = "unknown"
	auto.Candidates[1].ContextLength = nil
	fill()
	require.Zero(t, a.Info().ContextLimit)
}
