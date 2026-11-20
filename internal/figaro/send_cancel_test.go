package figaro

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

type lateAssistantProvider struct{}

func (lateAssistantProvider) Name() string                                         { return "late" }
func (lateAssistantProvider) Fingerprint() string                                  { return "late/v1" }
func (lateAssistantProvider) SetModel(string)                                      {}
func (lateAssistantProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (lateAssistantProvider) Send(_ context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushFigaro(message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("late")}})
	return nil
}

func TestACancelledSendReturnsTheCancellationNotAPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := guardedSend(ctx, lateAssistantProvider{}, provider.SendInput{}, newTurnBus(ctx))
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, err.Error(), "panic")
}

type panickingProvider struct{ lateAssistantProvider }

func (panickingProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	panic("boom")
}

func TestARealPanicStillSurfacesWithItsStack(t *testing.T) {
	err := guardedSend(context.Background(), panickingProvider{}, provider.SendInput{}, newTurnBus(context.Background()))
	require.ErrorContains(t, err, "provider send panic: boom")
}
