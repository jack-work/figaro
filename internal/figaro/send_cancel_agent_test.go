package figaro_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

type hangingProvider struct {
	mockProvider
	started chan struct{}
	late    bool
}

func (p *hangingProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	close(p.started)
	<-ctx.Done()
	if p.late {
		bus.PushFigaro(message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("too late")}})
	}
	return ctx.Err()
}

func TestAKilledTurnEndsInterruptedWithNoFailureOnTheForm(t *testing.T) {
	for _, late := range []bool{false, true} {
		seed := form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.model":    json.RawMessage(`"mock-model-v1"`),
			"system.provider": json.RawMessage(`"mock"`),
		}, nil)
		cb, _ := form.Open("")
		cb.Apply(seed)
		be, id := store.NewTestAria(t, "d", seed)
		p := &hangingProvider{started: make(chan struct{}), late: late}
		a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Provider: p, Backend: be, Form: cb})
		ch, _ := subscribeChan(a)
		submitPrompt(a, "go")
		<-p.started
		a.Kill()

		require.Equal(t, "interrupted", waitDoneReason(t, ch), "late=%v", late)
		_, failed := durableFailure(t, be, id)
		require.False(t, failed, "a cancelled send recorded a failure (late=%v)", late)
	}
}
