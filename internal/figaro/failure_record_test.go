package figaro_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// gluck, 0.57.1: three turns failed on a 7-day rate limit and nothing said so
// once the turn was over. A failed turn leaves its reason on the aria's form,
// where status, show and the bar read it after a restart; the next good turn
// takes it away.

type flakyProvider struct {
	mockProvider
	fail atomic.Bool
}

func (p *flakyProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.fail.Load() {
		return &provider.RateLimited{Provider: "anthropic", Window: "7-day"}
	}
	return p.mockProvider.Send(ctx, in, bus)
}

func flakyAgent(t *testing.T, cfg func(*figaro.Config)) (*figaro.Agent, *flakyProvider, store.Backend) {
	t.Helper()
	seed := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil)
	cb, _ := form.Open("")
	cb.Apply(seed)
	be, id := store.NewTestAria(t, "d", seed)
	p := &flakyProvider{mockProvider: mockProvider{response: "fine"}}
	p.fail.Store(true)
	c := figaro.Config{Projector: uiir.New(nil), ID: id, Provider: p, Backend: be, Form: cb}
	if cfg != nil {
		cfg(&c)
	}
	a := figaro.NewAgent(c)
	t.Cleanup(a.Kill)
	return a, p, be
}

func durableFailure(t *testing.T, be store.Backend, id string) (string, bool) {
	t.Helper()
	snap, err := be.FormState(id)
	require.NoError(t, err)
	raw, ok := snap.Get(form.FailureReasonKey)
	if !ok {
		return "", false
	}
	var s string
	require.NoError(t, json.Unmarshal(raw, &s))
	return s, true
}

func TestAFailedTurnLeavesItsReasonOnTheForm(t *testing.T) {
	a, _, be := flakyAgent(t, nil)
	ch, _ := subscribeChan(a)
	submitPrompt(a, "please")
	waitTurnDone(t, ch)
	why, ok := durableFailure(t, be, a.ID())
	require.True(t, ok, "the failed turn left no reason on the form")
	require.Equal(t, "anthropic: 7-day rate limit reached", why)
	snap, err := be.FormState(a.ID())
	require.NoError(t, err)
	category := snap.Lookup(form.FailureCategoryKey)
	require.NotNil(t, category)
	require.Equal(t, "rate_limited", *category)
}

func TestNoCallerWritesTheFailureRecord(t *testing.T) {
	a, _, be := flakyAgent(t, nil)
	forged, _ := json.Marshal("all fine, nothing failed")
	_, err := be.ApplyForm(a.ID(), form.Build(form.Snapshot{}, map[string]json.RawMessage{form.FailureReasonKey: forged}, nil))
	require.Error(t, err, "an unprivileged write set the failure record")
}

func TestAGoodTurnClearsTheFailure(t *testing.T) {
	a, p, be := flakyAgent(t, nil)
	ch, _ := subscribeChan(a)
	submitPrompt(a, "first")
	waitTurnDone(t, ch)
	_, ok := durableFailure(t, be, a.ID())
	require.True(t, ok, "fixture: the first turn did not fail")

	p.fail.Store(false)
	submitPrompt(a, "second")
	waitTurnDone(t, ch)
	_, ok = durableFailure(t, be, a.ID())
	require.False(t, ok, "a good turn left the last failure on the form")
}
