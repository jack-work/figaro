package anthropicsdk

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// A turn cut short mid-tool-call leaves a partial assistant message and
// synthesised tool_results in the IR. This asserts on the thing that actually
// goes to the model: THE BUILT REQUEST.
//
// The IR being well-formed is not the same claim. Between the IR and the wire
// sit renderMessage (which drops blocks it cannot represent — thinking, and any
// message that renders to nothing), coalesceMessages (which merges adjacent
// same-role messages), and the translation cache. Any of the three could break
// the pairing Anthropic requires — every tool_use answered by a tool_result
// with the same id — and the failure mode is an HTTP 400 on the NEXT turn, i.e.
// a conversation that is dead and cannot say why.

// interruptedToolProvider issues one tool call, appends the assistant message
// (so the turn is "committed" and the repair path is the tool-results one),
// then parks until the turn is cancelled.
type interruptedToolProvider struct{ started chan struct{} }

func (p *interruptedToolProvider) Name() string        { return "interrupted-tool" }
func (p *interruptedToolProvider) Fingerprint() string { return "interrupted-tool/v0" }
func (p *interruptedToolProvider) SetModel(string)     {}
func (p *interruptedToolProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *interruptedToolProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	call := message.Content{
		Type: message.ContentToolInvoke, ToolCallID: "toolu_cut", ToolName: "park",
		Arguments: map[string]any{},
	}
	bus.PushDelta(message.Content{Type: message.ContentThinking, Text: "half a thought"})
	bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
	bus.PushToolReady(call)
	msg := message.Message{
		Role: message.RoleOutput, StopReason: message.StopToolInvoke,
		Content:   []message.Content{{Type: message.ContentThinking, Text: "half a thought"}, call},
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

// parkTool blocks until cancelled, so the interrupt lands with a tool in flight.
type parkTool struct{ started chan struct{} }

func (t *parkTool) Name() string        { return "park" }
func (t *parkTool) Description() string { return "parks until cancelled" }
func (t *parkTool) Parameters() any     { return map[string]any{"type": "object"} }
func (t *parkTool) Execute(ctx context.Context, _ map[string]any, out tool.OnOutput) ([]message.Content, error) {
	out([]byte("partial output before the cut"))
	close(t.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestInterruptedTurn_BuiltRequestPairsEveryToolUse(t *testing.T) {
	dir := t.TempDir()
	backend, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	defer backend.Close()

	outfit, err := backend.CreateOutfit("d", message.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m"`),
		"system.provider": json.RawMessage(`"interrupted-tool"`),
	}})
	require.NoError(t, err)
	conv, err := backend.CreateConversation(outfit)
	require.NoError(t, err)

	toolStarted := make(chan struct{})
	registry := tool.NewRegistry()
	registry.MustRegister(&parkTool{started: toolStarted})

	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         conv,
		SocketPath: filepath.Join(t.TempDir(), "a.sock"),
		Provider:   &interruptedToolProvider{},
		Backend:    backend,
		Tools:      registry,
	})
	defer a.Kill()

	a.SubmitPrompt(rpc.QuaRequest{Text: "call the tool"})
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never started; nothing was in flight to interrupt")
	}

	a.Interrupt()

	// Wait for the repair to land: the tool_result tic is appended by
	// repairTurnTail as the turn unwinds.
	var msgs []message.Message
	require.Eventually(t, func() bool {
		msgs = a.Context()
		return countBlocks(msgs, message.ContentToolResult) > 0
	}, 5*time.Second, 25*time.Millisecond, "the interrupted turn never closed its tool call")

	// Rebuild the IR into a log and project it exactly as a live Send would.
	figLog := store.NewMemLog[message.Message]()
	for _, m := range msgs {
		_, err := figLog.Append(store.Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}

	p := &Provider{}
	projection, err := p.catchUp(figLog, nil, nil)
	require.NoError(t, err, "the post-interrupt IR must project to a request at all")

	assertToolUsePairing(t, projection.Messages)
}

// assertToolUsePairing is the invariant the API enforces and a 400 reports
// far too late: every tool_use block is answered by a tool_result block with
// the same id, in a LATER message.
func assertToolUsePairing(t *testing.T, msgs []anthropic.MessageParam) {
	t.Helper()
	uses := map[string]int{}
	results := map[string]int{}
	for i, m := range msgs {
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		var decoded struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		for _, c := range decoded.Content {
			switch c.Type {
			case "tool_use":
				uses[c.ID] = i
			case "tool_result":
				results[c.ToolUseID] = i
			}
		}
	}
	require.NotEmpty(t, uses, "the fixture must actually put a tool_use on the wire")
	for id, at := range uses {
		answeredAt, ok := results[id]
		require.True(t, ok,
			"tool_use %q reaches the model with no tool_result — the next request is a 400", id)
		require.Greater(t, answeredAt, at,
			"tool_result for %q must come AFTER the tool_use", id)
	}
}

func countBlocks(msgs []message.Message, kind message.ContentType) int {
	n := 0
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == kind {
				n++
			}
		}
	}
	return n
}
