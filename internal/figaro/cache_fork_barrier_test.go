package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type sealBarrierProvider struct {
	afterAck chan struct{}
	release  chan struct{}
}

func (p *sealBarrierProvider) Name() string        { return "seal-barrier" }
func (p *sealBarrierProvider) Fingerprint() string { return "seal-barrier/v1" }
func (p *sealBarrierProvider) SetModel(string)     {}
func (p *sealBarrierProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *sealBarrierProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("sealed")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	_, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	bus.PushFigaro(msg, provider.AssistantCache{
		Namespace:   "seal-barrier",
		Payload:     []json.RawMessage{json.RawMessage(`{"native":"sealed"}`)},
		Fingerprint: p.Fingerprint(),
	})
	close(p.afterAck)
	<-p.release
	return nil
}

func TestQueuedForkWaitsForProviderCacheSeal(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	prov := &sealBarrierProvider{
		afterAck: make(chan struct{}), release: make(chan struct{}),
	}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	select {
	case <-prov.afterAck:
	case <-time.After(5 * time.Second):
		t.Fatal("assistant IR was not acknowledged")
	}

	var cont, alt string
	forkDone := make(chan error, 1)
	go func() {
		forkDone <- a.CoordinateFork(func() error {
			var err error
			cont, alt, err = b.Fork(id)
			return err
		})
	}()
	select {
	case err := <-forkDone:
		t.Fatalf("fork crossed provider cache barrier: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(prov.release)
	require.NoError(t, <-forkDone)
	waitDone(t, ch)

	ids := []string{cont, alt}
	for _, branch := range ids {
		ir, err := b.Open(branch)
		require.NoError(t, err)
		tail, ok := ir.PeekTail()
		require.True(t, ok)
		assert.Equal(t, message.RoleAssistant, tail.Payload.Role)
		cache, err := b.OpenTranslation(branch, "seal-barrier")
		require.NoError(t, err)
		cached, ok := cache.Lookup(tail.LT)
		require.True(t, ok, "cache missing on branch %s", branch)
		assert.JSONEq(t, `{"native":"sealed"}`, string(cached.Payload[0]))
	}
}
