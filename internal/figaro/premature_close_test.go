package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// prematureCloseProvider hands over its own accumulator on cancel, marked aborted, with its native payload.
type prematureCloseProvider struct {
	started chan struct{}
}

func (p *prematureCloseProvider) Name() string        { return "premature" }
func (p *prematureCloseProvider) Fingerprint() string { return "premature/v1" }
func (p *prematureCloseProvider) SetModel(string)     {}
func (p *prematureCloseProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *prematureCloseProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: "half a thou"})
	close(p.started)
	<-ctx.Done()

	bus.PushMessageEnd(string(message.StopAborted))
	bus.PushFigaro(message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("half a thou")},
		StopReason: message.StopAborted,
		Timestamp:  time.Now().UnixMilli(),
	}, provider.AssistantCache{
		Namespace:   "premature",
		Fingerprint: "premature/v1",
		Payload:     []json.RawMessage{json.RawMessage(`{"role":"assistant","native":true}`)},
	})
	return ctx.Err()
}

// A provider that closes early owns its partial message, and its native
// payload (thinking signatures) survives instead of being re-encoded.
func TestAProviderThatClosesEarlyKeepsItsNativePayload(t *testing.T) {
	be, id := store.NewTestAria(t, "d", message.Patch{})
	prov := &prematureCloseProvider{started: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil), ID: id, Provider: prov, Backend: be, Tools: tool.NewRegistry(),
	})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})

	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	a.Interrupt()
	waitDoneReason(t, ch)

	log, err := be.OpenFigIR(id)
	require.NoError(t, err)
	var last message.Message
	for _, e := range log.Read() {
		if e.Payload.Role == message.RoleOutput {
			last = e.Payload
		}
	}
	require.NotEmpty(t, last.Content, "the interrupted assistant never reached the fig IR")
	require.Equal(t, message.StopAborted, last.StopReason)
	require.Equal(t, "half a thou", last.Content[0].Text)

	// The native payload distinguishes the provider-owned close from the repair.
	trans, err := be.OpenTranslator(id, "premature")
	require.NoError(t, err)
	entries := trans.Read()
	require.NotEmpty(t, entries, "no translation for the interrupted assistant")
	require.Contains(t, string(entries[len(entries)-1].Payload[0]), `"native":true`)
}
