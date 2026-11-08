package figaro_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

var errReset = errors.New("stream error: stream ID 697; CANCEL; received from peer")

type cutProvider struct {
	mu      sync.Mutex
	cuts    int
	calls   int
	readyAt int
	seen    [][]message.Message
}

func (*cutProvider) Name() string                                         { return "cut-test" }
func (*cutProvider) Fingerprint() string                                  { return "cut-test/v1" }
func (*cutProvider) SetModel(string)                                      {}
func (*cutProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *cutProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	var history []message.Message
	for _, e := range in.FigLog.ReadFrom(0, 1<<20) {
		history = append(history, e.Payload)
	}
	p.seen = append(p.seen, history)
	cut := call <= p.cuts
	p.mu.Unlock()
	if !cut {
		bus.PushDelta(message.TextContent("whole reply"))
		bus.PushFigaro(message.Message{
			Role: message.RoleOutput, StopReason: message.StopEnd,
			Content: []message.Content{message.TextContent("whole reply")}, Timestamp: time.Now().UnixMilli(),
		})
		return nil
	}
	bus.PushDelta(message.TextContent("partial prose"))
	bus.PushToolInvokeStart("write-1", "write")
	bus.PushToolInvokeDelta("write-1", `{"path":"test/pdf-pa`)
	if p.readyAt == call {
		bus.PushToolReady(message.Content{
			Type: message.ContentToolInvoke, ToolCallID: "write-1", ToolName: "write", Arguments: map[string]any{},
		})
	}
	return &provider.StreamCut{Provider: "copilot", Awaited: "message_stop", Events: 12, Err: errReset}
}

func runCutTurn(t *testing.T, prov *cutProvider, tools *tool.Registry) (*figaro.Agent, string) {
	t.Helper()
	b, id := newBackedConversation(t)
	t.Cleanup(func() { b.Close() })
	if tools == nil {
		tools = tool.NewRegistry()
	}
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Provider: prov, Backend: b, Tools: tools})
	t.Cleanup(a.Kill)
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	return a, waitDoneReason(t, ch)
}

// The 11-08 opium-census failure: copilot reset the stream while a write call
// was still being written. Nothing had run, so the turn must carry on.
func TestAStreamCutBeforeAnyToolRunsResumesTheTurn(t *testing.T) {
	defer figaro.SetStreamCutBackoffForTest(time.Millisecond, time.Millisecond)()
	prov := &cutProvider{cuts: 1}
	a, reason := runCutTurn(t, prov, nil)

	assert.Equal(t, string(message.StopEnd), reason)
	assert.Equal(t, 2, prov.calls)
	got := a.Context()
	require.GreaterOrEqual(t, len(got), 4)
	got = got[len(got)-4:]
	assert.Equal(t, "go", got[0].Content[0].Text)
	assert.Equal(t, message.StopAborted, got[1].StopReason)
	require.Len(t, got[1].Content, 1, "the half-written tool call is dropped, the prose kept")
	assert.Equal(t, "partial prose", got[1].Content[0].Text)
	assert.True(t, got[2].Steering)
	notice := got[2].Content[0]
	assert.Equal(t, "angelus", notice.Sender)
	assert.True(t, strings.HasPrefix(notice.Text, "continue: copilot: the upstream cut the stream after 12 events"), notice.Text)
	assert.Contains(t, notice.Text, "CANCEL")
	assert.Equal(t, "whole reply", got[3].Content[0].Text)

	first, resent := prov.seen[0], prov.seen[1]
	require.Len(t, resent, len(first)+2, "the resend carries the kept partial and the notice")
	assert.Equal(t, got[2].Content[0].Text, resent[len(resent)-1].Content[0].Text)
}

func TestStreamCutsPastTheBudgetFailTheTurnWithTheCause(t *testing.T) {
	defer figaro.SetStreamCutBackoffForTest(time.Millisecond, time.Millisecond)()
	prov := &cutProvider{cuts: 3}
	_, reason := runCutTurn(t, prov, nil)

	assert.Equal(t, 3, prov.calls)
	assert.Contains(t, reason, "copilot: the upstream cut the stream after 12 events")
	assert.Contains(t, reason, "CANCEL")
}

type countingTool struct {
	mu   sync.Mutex
	runs int
}

func (*countingTool) Name() string        { return "write" }
func (*countingTool) Description() string { return "counts runs" }
func (*countingTool) Parameters() any     { return map[string]any{"type": "object"} }
func (c *countingTool) Execute(context.Context, map[string]any, tool.OnOutput) ([]message.Content, error) {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	return []message.Content{message.TextContent("wrote")}, nil
}

func TestAStreamCutAfterAToolStartedIsNotResent(t *testing.T) {
	defer figaro.SetStreamCutBackoffForTest(time.Millisecond, time.Millisecond)()
	prov := &cutProvider{cuts: 1, readyAt: 1}
	tools := tool.NewRegistry()
	tools.MustRegister(&countingTool{})
	_, reason := runCutTurn(t, prov, tools)

	assert.Equal(t, 1, prov.calls, "a resend could run the tool twice")
	assert.Contains(t, reason, "copilot: the upstream cut the stream")
}
