package figaro_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
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

type multiRoundProvider struct {
	mode       string
	round      atomic.Int32
	roundTwo   chan struct{}
	toolOutput chan struct{}
}

func (p *multiRoundProvider) Name() string        { return "multi-round" }
func (p *multiRoundProvider) Fingerprint() string { return "multi-round/v1" }
func (p *multiRoundProvider) SetModel(string)     {}
func (p *multiRoundProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *multiRoundProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	round := p.round.Add(1)
	if round == 1 {
		return pushToolAssistant(in, bus, "call-1", map[string]any{"round": float64(1)})
	}
	if p.mode == "prose" {
		bus.PushDelta(message.TextContent("round two prose"))
		close(p.roundTwo)
		<-ctx.Done()
		return ctx.Err()
	}
	return pushToolAssistant(in, bus, "call-2", map[string]any{"round": float64(2)})
}

func pushToolAssistant(in provider.SendInput, bus provider.Bus, id string, args map[string]any) error {
	call := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "round-tool", Arguments: args,
	}
	bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
	bus.PushToolReady(call)
	msg := message.Message{
		Role: message.RoleAssistant, StopReason: message.StopToolInvoke,
		Content: []message.Content{call}, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

type multiRoundTool struct {
	output chan struct{}
}

func (t *multiRoundTool) Name() string        { return "round-tool" }
func (t *multiRoundTool) Description() string { return "round test tool" }
func (t *multiRoundTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *multiRoundTool) Execute(ctx context.Context, args map[string]any, out tool.OnOutput) ([]message.Content, error) {
	if args["round"] == float64(1) {
		return []message.Content{message.TextContent("round one complete")}, nil
	}
	out([]byte("round two tool output"))
	close(t.output)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRoundTwoInterruptUsesFreshCheckpoint(t *testing.T) {
	for _, mode := range []string{"prose", "tool"} {
		t.Run(mode, func(t *testing.T) {
			b, id := newBackedConversation(t)
			defer b.Close()
			prov := &multiRoundProvider{
				mode: mode, roundTwo: make(chan struct{}), toolOutput: make(chan struct{}),
			}
			registry := tool.NewRegistry()
			registry.MustRegister(&multiRoundTool{output: prov.toolOutput})
			a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: registry})
			defer a.Kill()
			ch, _ := subscribeChan(a)
			a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
			if mode == "prose" {
				select {
				case <-prov.roundTwo:
				case <-time.After(5 * time.Second):
					t.Fatal("round two prose did not start")
				}
			} else {
				select {
				case <-prov.toolOutput:
				case <-time.After(5 * time.Second):
					t.Fatal("round two tool did not stream")
				}
			}
			time.Sleep(100 * time.Millisecond)

			ir, err := b.Open(id)
			require.NoError(t, err)
			tail, ok := ir.PeekTail()
			require.True(t, ok)
			journal, err := b.OpenTurnJournal(id)
			require.NoError(t, err)
			payload, ok, err := journal.Latest(tail.LT + 1)
			require.NoError(t, err)
			require.True(t, ok)
			var checkpoint map[string]any
			require.NoError(t, json.Unmarshal(payload, &checkpoint))
			assert.GreaterOrEqual(t, checkpoint["generation"].(float64), float64(2))

			a.Interrupt()
			waitDone(t, ch)
			history := a.Context()
			if mode == "prose" {
				last := history[len(history)-1]
				assert.Equal(t, message.StopAborted, last.StopReason)
				assert.Equal(t, "round two prose", last.Content[0].Text)
			} else {
				last := history[len(history)-1]
				assert.Equal(t, message.ContentToolResult, last.Content[0].Type)
				assert.Contains(t, last.Content[0].Text, "round two tool output")
			}
		})
	}
}

func TestRoundTwoRestartRecovery(t *testing.T) {
	for _, mode := range []string{"prose", "tool"} {
		t.Run(mode, func(t *testing.T) {
			b, id := newBackedConversation(t)
			defer b.Close()
			ir, err := b.Open(id)
			require.NoError(t, err)
			firstCall := message.Content{
				Type: message.ContentToolInvoke, ToolCallID: "call-1", ToolName: "round-tool",
				Arguments: map[string]any{"round": float64(1)},
			}
			_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleAssistant, StopReason: message.StopToolInvoke, Content: []message.Content{firstCall},
			}})
			require.NoError(t, err)
			_, err = ir.Append(store.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleUser,
				Content: []message.Content{
					message.ToolResultContent("call-1", "round-tool", "round one complete", false),
				},
			}})
			require.NoError(t, err)
			journal, err := b.OpenTurnJournal(id)
			require.NoError(t, err)

			if mode == "prose" {
				tail, _ := ir.PeekTail()
				target := tail.LT + 1
				require.NoError(t, journal.Checkpoint(target, checkpointJSON(
					t, target, "assistant", message.Message{
						Role: message.RoleAssistant, Content: []message.Content{message.TextContent("round two prose")},
					}, nil,
				)))
			} else {
				secondCall := message.Content{
					Type: message.ContentToolInvoke, ToolCallID: "call-2", ToolName: "round-tool",
					Arguments: map[string]any{"round": float64(2)},
				}
				assistant, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
					Role: message.RoleAssistant, StopReason: message.StopToolInvoke, Content: []message.Content{secondCall},
				}})
				require.NoError(t, err)
				target := assistant.LT + 1
				require.NoError(t, journal.Checkpoint(target, checkpointJSON(
					t, target, "tools", assistant.Payload, []map[string]any{{
						"tool_call_id": "call-2", "tool_name": "round-tool",
						"status": "running", "output_tail": "round two tool output",
					}},
				)))
			}
			require.NoError(t, journal.Sync())

			prov := &recoveryProvider{}
			a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
			history := a.Context()
			a.Kill()
			if mode == "prose" {
				assert.Equal(t, "round two prose", history[len(history)-1].Content[0].Text)
				assert.Equal(t, message.StopAborted, history[len(history)-1].StopReason)
			} else {
				assert.Contains(t, history[len(history)-1].Content[0].Text, "round two tool output")
			}
			assert.Zero(t, prov.callCount())
		})
	}
}
