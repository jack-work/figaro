package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// prematureCloseProvider streams, then -- when the turn is cancelled -- hands
// over WHAT IT HAS from its own accumulator, marked aborted, with its native
// payload. That is section 3(c): the interrupt path and the normal path
// produce a message the same way, differing only in the stop reason.
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

	// THE PREMATURE CLOSE. The accumulator is the provider's own, so the
	// message and its native payload cannot disagree -- which is the whole
	// point of moving this out of figaro.
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

// A PROVIDER THAT CLOSES EARLY OWNS ITS PARTIAL MESSAGE, AND ITS NATIVE
// PAYLOAD SURVIVES.
//
// figaro used to synthesise the partial from its own accumulator, which has
// no provider-native material in it: the fig IR entry existed but its
// translation had to be RE-ENCODED, losing thinking signatures and encrypted
// reasoning. When the provider hands over the same message it would have sent
// on a normal close, the native payload comes with it.
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

	// THE NATIVE PAYLOAD IS THE POINT. A figaro-synthesised partial has none,
	// so this is what distinguishes the provider-owned close from the repair.
	trans, err := be.OpenTranslator(id, "premature")
	require.NoError(t, err)
	entries := trans.Read()
	require.NotEmpty(t, entries, "no translation for the interrupted assistant")
	require.Contains(t, string(entries[len(entries)-1].Payload[0]), `"native":true`)
}
